mod collectors;
mod db;
mod intelligence;
mod models;

use async_nats::Subscriber;
use collectors::{ct, dns, http, rdap, tls};
use collectors::domain::registrable_domain;
use db::{
    create_pool, insert_event, mark_job_completed, mark_job_failed, mark_job_running,
    persist_collector_result,
};
use futures::StreamExt;
use intelligence::persist_collector_intelligence;
use models::{CrawlJob, EnrichDomainJob};
use std::{env, sync::Arc, time::Duration};
use tokio::sync::Semaphore;

#[tokio::main]
async fn main() {
    rustls::crypto::ring::default_provider()
        .install_default()
        .expect("rustls crypto provider");

    let worker_id = env::var("HOSTNAME").unwrap_or_else(|_| format!("worker-{}", std::process::id()));
    let nats_url = env::var("NATS_URL").unwrap_or_else(|_| "nats://nats:4222".into());
    let database_url = env::var("DATABASE_URL")
        .unwrap_or_else(|_| "postgres://atlas:atlas@postgres:5432/atlas?sslmode=disable".into());
    let concurrency: usize = env::var("WORKER_CONCURRENCY")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(20)
        .clamp(1, 100);

    let nats = Arc::new(async_nats::connect(&nats_url).await.expect("nats connection"));
    let pool = Arc::new(create_pool(&database_url));
    let http_client = Arc::new(
        reqwest::Client::builder()
            .redirect(reqwest::redirect::Policy::limited(10))
            .timeout(Duration::from_secs(15))
            .user_agent("AtlasDiscovery/0.1")
            .build()
            .expect("http client"),
    );
    let dns_resolver = Arc::new(dns::create_resolver());
    let semaphore = Arc::new(Semaphore::new(concurrency));

    println!(
        "atlas worker {} online (concurrency={})",
        worker_id, concurrency
    );

    for collector in ["dns", "http", "tls", "ct", "rdap"] {
        let nats = nats.clone();
        let pool = pool.clone();
        let http_client = http_client.clone();
        let dns_resolver = dns_resolver.clone();
        let semaphore = semaphore.clone();
        let worker_id = worker_id.clone();
        let subject = format!("atlas.jobs.{}", collector);

        tokio::spawn(async move {
            let sub: Subscriber = nats
                .queue_subscribe(subject.clone(), "atlas-workers".to_string())
                .await
                .unwrap_or_else(|e| panic!("subscribe {}: {}", subject, e));

            let mut sub = sub;
            while let Some(message) = sub.next().await {
                let permit = semaphore.clone().acquire_owned().await.unwrap();
                let pool = pool.clone();
                let http_client = http_client.clone();
                let dns_resolver = dns_resolver.clone();
                let worker_id = worker_id.clone();
                let payload = message.payload.to_vec();

                tokio::spawn(async move {
                    let _permit = permit;
                    if let Err(e) = handle_message(
                        &pool,
                        &http_client,
                        &dns_resolver,
                        &worker_id,
                        &payload,
                    )
                    .await
                    {
                        eprintln!("job error: {}", e);
                    }
                });
            }
        });
    }

    {
        let nats = nats.clone();
        let pool = pool.clone();
        let http_client = http_client.clone();
        let dns_resolver = dns_resolver.clone();
        let semaphore = semaphore.clone();

        let semaphore = semaphore.clone();

        tokio::spawn(async move {
            let sub: Subscriber = nats
                .queue_subscribe("atlas.enrich.domain".to_string(), "atlas-enrichers".to_string())
                .await
                .expect("subscribe atlas.enrich.domain");

            let mut sub = sub;
            while let Some(message) = sub.next().await {
                let permit = semaphore.clone().acquire_owned().await.unwrap();
                let pool = pool.clone();
                let http_client = http_client.clone();
                let dns_resolver = dns_resolver.clone();
                let payload = message.payload.to_vec();

                tokio::spawn(async move {
                    let _permit = permit;
                    if let Err(e) = handle_enrich(&pool, &http_client, &dns_resolver, &payload).await {
                        eprintln!("enrich error: {}", e);
                    }
                });
            }
        });
    }

    std::future::pending::<()>().await;
}

async fn handle_message(
    pool: &deadpool_postgres::Pool,
    http_client: &reqwest::Client,
    dns_resolver: &hickory_resolver::TokioAsyncResolver,
    worker_id: &str,
    payload: &[u8],
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let job: CrawlJob = serde_json::from_slice(payload)?;

    mark_job_running(pool, &job.job_id).await?;

    insert_event(
        pool,
        &job.campaign_id,
        "collector.started",
        &format!("{} collector started for {}", job.collector, job.entity_value),
        serde_json::json!({
            "job_id": job.job_id,
            "collector": job.collector,
            "worker_id": worker_id,
        }),
    )
    .await?;

    let result = match job.collector.as_str() {
        "dns" => dns::collect(dns_resolver, &job).await,
        "http" => http::collect(http_client, &job).await,
        "tls" => tls::collect(&job).await,
        "ct" => ct::collect(pool, &job).await,
        "rdap" => rdap::collect(http_client, pool, &job).await,
        other => Err(format!("unknown collector: {}", other).into()),
    };

    match result {
        Ok(collector_result) => {
            persist_collector_result(pool, &job, &collector_result).await?;
            if let Err(e) = persist_collector_intelligence(pool, &job, &collector_result).await {
                eprintln!("intelligence persist warning: {}", e);
            }
            mark_job_completed(pool, &job.job_id).await?;
        }
        Err(err) => {
            let message = err.to_string();
            insert_event(
                pool,
                &job.campaign_id,
                "collector.failed",
                &format!("{} collector failed for {}: {}", job.collector, job.entity_value, message),
                serde_json::json!({
                    "job_id": job.job_id,
                    "collector": job.collector,
                    "error": message,
                }),
            )
            .await?;
            mark_job_failed(pool, &job.job_id, &message).await?;
        }
    }

    Ok(())
}

async fn handle_enrich(
    pool: &deadpool_postgres::Pool,
    http_client: &reqwest::Client,
    dns_resolver: &hickory_resolver::TokioAsyncResolver,
    payload: &[u8],
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let job: EnrichDomainJob = serde_json::from_slice(payload)?;
    let apex = registrable_domain(&job.domain);
    if apex.is_empty() {
        return Err("empty apex domain".into());
    }
    let collectors = if job.collectors.is_empty() {
        vec!["ct".into(), "rdap".into(), "dns".into()]
    } else {
        job.collectors.clone()
    };

    let crawl_job = CrawlJob {
        job_id: String::new(),
        campaign_id: String::new(),
        entity_id: String::new(),
        entity_type: "domain".into(),
        entity_value: job.domain.clone(),
        collector: String::new(),
        depth: 0,
    };

    for collector in collectors {
        let mut cj = crawl_job.clone();
        cj.collector = collector.clone();

        let result = match collector.as_str() {
            "dns" => dns::collect(dns_resolver, &cj).await,
            "http" => http::collect(http_client, &cj).await,
            "tls" => tls::collect(&cj).await,
            "ct" => ct::collect(pool, &cj).await,
            "rdap" => rdap::collect(http_client, pool, &cj).await,
            _ => continue,
        };

        match result {
            Ok(collector_result) => {
                intelligence::persist_enrichment(pool, &apex, &collector, &collector_result).await?;
            }
            Err(e) => {
                eprintln!("enrich {} for {}: {}", collector, job.domain, e);
            }
        }
    }

    Ok(())
}
