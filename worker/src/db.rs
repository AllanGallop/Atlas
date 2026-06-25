use crate::models::{CampaignLimits, CollectorResult, CrawlJob};
use deadpool_postgres::{Pool, Runtime};
use serde_json::json;
use std::collections::HashMap;
use tokio_postgres::types::Json;
use tokio_postgres::NoTls;
use uuid::Uuid;

fn new_id(prefix: &str) -> String {
    format!("{}_{}", prefix, Uuid::new_v4())
}

pub fn create_pool(database_url: &str) -> Pool {
    let mut cfg = deadpool_postgres::Config::new();
    cfg.url = Some(database_url.to_string());
    cfg.create_pool(Some(Runtime::Tokio1), NoTls).expect("postgres pool")
}

pub async fn mark_job_running(pool: &Pool, job_id: &str) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let client = pool.get().await?;
    client
        .execute(
            "UPDATE crawl_jobs SET status = 'running', started_at = NOW(), attempts = attempts + 1 WHERE id = $1",
            &[&job_id],
        )
        .await?;
    Ok(())
}

pub async fn mark_job_completed(pool: &Pool, job_id: &str) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let client = pool.get().await?;
    client
        .execute(
            "UPDATE crawl_jobs SET status = 'completed', completed_at = NOW(), error = NULL WHERE id = $1",
            &[&job_id],
        )
        .await?;
    Ok(())
}

pub async fn mark_job_failed(pool: &Pool, job_id: &str, error: &str) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let client = pool.get().await?;
    client
        .execute(
            "UPDATE crawl_jobs SET status = 'failed', completed_at = NOW(), error = $2 WHERE id = $1",
            &[&job_id, &error],
        )
        .await?;
    Ok(())
}

pub async fn get_campaign_limits(pool: &Pool, campaign_id: &str) -> Result<CampaignLimits, Box<dyn std::error::Error + Send + Sync>> {
    let client = pool.get().await?;
    let row = client
        .query_one(
            "SELECT max_depth, max_entities FROM campaigns WHERE id = $1",
            &[&campaign_id],
        )
        .await?;
    Ok(CampaignLimits {
        max_depth: row.get(0),
        max_entities: row.get(1),
    })
}

pub async fn persist_collector_result(
    pool: &Pool,
    job: &CrawlJob,
    result: &CollectorResult,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let mut client = pool.get().await?;
    let tx = client.transaction().await?;

    let observation_id = new_id("obs");
    let raw_json = Json(&result.raw);
    tx.execute(
        "INSERT INTO observations (id, campaign_id, collector, entity_id, observed_at, raw_json, source)
         VALUES ($1, $2, $3, $4, NOW(), $5, $6)",
        &[
            &observation_id,
            &job.campaign_id,
            &job.collector,
            &job.entity_id,
            &raw_json,
            &result.source,
        ],
    )
    .await?;

    let limits = get_campaign_limits(pool, &job.campaign_id).await?;
    let entity_count: i32 = tx
        .query_one(
            "SELECT COUNT(*)::int FROM campaign_entities WHERE campaign_id = $1",
            &[&job.campaign_id],
        )
        .await?
        .get(0);

  let mut seen: HashMap<(String, String), String> = HashMap::new();

    for discovery in &result.discoveries {
        let key = (discovery.entity_type.clone(), discovery.value.clone());
        let child_id = if let Some(id) = seen.get(&key) {
            id.clone()
        } else {
            let id = upsert_entity_tx(&tx, &discovery.entity_type, &discovery.value).await?;
            seen.insert(key, id.clone());
            id
        };

        let next_depth = job.depth + 1;
        tx.execute(
            "INSERT INTO campaign_entities (campaign_id, entity_id, depth)
             VALUES ($1, $2, $3)
             ON CONFLICT (campaign_id, entity_id) DO UPDATE
             SET depth = LEAST(campaign_entities.depth, EXCLUDED.depth)",
            &[&job.campaign_id, &child_id, &next_depth],
        )
        .await?;

        let edge_id = new_id("edge");
        let evidence_value = json!([observation_id.clone()]);
        let evidence = Json(&evidence_value);
        tx.execute(
            "INSERT INTO edges (id, campaign_id, from_entity_id, relation, to_entity_id, confidence, evidence_observation_ids)
             VALUES ($1, $2, $3, $4, $5, 1.0, $6)
             ON CONFLICT (campaign_id, from_entity_id, relation, to_entity_id) DO UPDATE
             SET last_seen_at = NOW(),
                 evidence_observation_ids = edges.evidence_observation_ids || EXCLUDED.evidence_observation_ids",
            &[
                &edge_id,
                &job.campaign_id,
                &job.entity_id,
                &discovery.relation,
                &child_id,
                &evidence,
            ],
        )
        .await?;

        if next_depth <= limits.max_depth && entity_count < limits.max_entities {
            if discovery.value.contains('*') {
                continue;
            }
            let collectors = suggested_collectors(&discovery.entity_type);
            let collectors_value = json!(collectors);
            let collectors_json = Json(&collectors_value);
            let suggestion_id = new_id("exp");
            tx.execute(
                "INSERT INTO expansion_suggestions (id, campaign_id, entity_id, depth, reason, suggested_collectors, status)
                 VALUES ($1, $2, $3, $4, $5, $6, 'pending')
                 ON CONFLICT (campaign_id, entity_id) DO UPDATE
                 SET reason = EXCLUDED.reason,
                     suggested_collectors = EXCLUDED.suggested_collectors,
                     depth = LEAST(expansion_suggestions.depth, EXCLUDED.depth)",
                &[
                    &suggestion_id,
                    &job.campaign_id,
                    &child_id,
                    &next_depth,
                    &format!("discovered via {} from {}", job.collector, job.entity_value),
                    &collectors_json,
                ],
            )
            .await?;
        }
    }

    let event_value = json!({
        "job_id": job.job_id,
        "collector": job.collector,
        "entity_id": job.entity_id,
        "discoveries": result.discoveries.len(),
    });
    let event_payload = Json(&event_value);
    tx.execute(
        "INSERT INTO campaign_events (campaign_id, event_type, message, payload)
         VALUES ($1, 'collector.completed', $2, $3)",
        &[
            &job.campaign_id,
            &format!("{} collector finished for {}", job.collector, job.entity_value),
            &event_payload,
        ],
    )
    .await?;

    tx.commit().await?;
    Ok(())
}

async fn upsert_entity_tx(
    tx: &tokio_postgres::Transaction<'_>,
    entity_type: &str,
    value: &str,
) -> Result<String, Box<dyn std::error::Error + Send + Sync>> {
    let id = new_id("ent");
    let row = tx
        .query_one(
            "INSERT INTO entities (id, type, value, first_seen_at, last_seen_at)
             VALUES ($1, $2, $3, NOW(), NOW())
             ON CONFLICT (type, value) DO UPDATE SET last_seen_at = NOW()
             RETURNING id",
            &[&id, &entity_type, &value],
        )
        .await?;
    Ok(row.get(0))
}

fn suggested_collectors(entity_type: &str) -> Vec<&'static str> {
    match entity_type {
        "domain" | "subdomain" => vec!["dns", "http", "tls", "ct", "rdap"],
        "ip" => vec!["http", "tls"],
        "nameserver" | "mx" => vec!["dns"],
        "url" => vec!["dns", "http", "tls"],
        _ => vec!["dns"],
    }
}

pub async fn insert_event(
    pool: &Pool,
    campaign_id: &str,
    event_type: &str,
    message: &str,
    payload: serde_json::Value,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let client = pool.get().await?;
    let payload_json = Json(&payload);
    client
        .execute(
            "INSERT INTO campaign_events (campaign_id, event_type, message, payload) VALUES ($1, $2, $3, $4)",
            &[&campaign_id, &event_type, &message, &payload_json],
        )
        .await?;
    Ok(())
}