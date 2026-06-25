use crate::collectors::domain::{classify_host, registrable_domain};
use crate::models::{CollectorResult, CrawlJob, DiscoveredEntity};
use deadpool_postgres::Pool;
use serde_json::Value;
use tokio_postgres::types::Json;
use uuid::Uuid;

fn new_id(prefix: &str) -> String {
    format!("{}_{}", prefix, Uuid::new_v4())
}

pub async fn persist_enrichment(
    pool: &Pool,
    domain: &str,
    collector: &str,
    result: &CollectorResult,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let domain_id = upsert_domain(pool, domain).await?;

    match collector {
        "rdap" => persist_rdap(pool, &domain_id, &result.raw).await?,
        "dns" => persist_dns(pool, &domain_id, domain, &result.raw).await?,
        "ct" => persist_ct(pool, &domain_id, domain, &result.raw).await?,
        "http" => persist_http(pool, &domain_id, domain, &result.raw).await?,
        "tls" => persist_tls(pool, &domain_id, domain, &result.raw).await?,
        _ => {}
    }

    Ok(())
}

pub async fn persist_collector_intelligence(
    pool: &Pool,
    job: &CrawlJob,
    result: &CollectorResult,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let apex = registrable_domain(&job.entity_value);
    if apex.is_empty() {
        return Ok(());
    }

    persist_enrichment(pool, &apex, &job.collector, result).await?;
    sync_campaign_discoveries(pool, job, result).await
}

async fn upsert_domain(pool: &Pool, domain: &str) -> Result<String, Box<dyn std::error::Error + Send + Sync>> {
    let mut client = pool.get().await?;
    let id = new_id("dom");
    let row = client
        .query_one(
            "INSERT INTO domains (id, domain, first_seen, last_seen)
             VALUES ($1, $2, NOW(), NOW())
             ON CONFLICT (domain) DO UPDATE SET last_seen = NOW()
             RETURNING id",
            &[&id, &domain],
        )
        .await?;
    Ok(row.get(0))
}

async fn upsert_graph_edge(
    tx: &tokio_postgres::Transaction<'_>,
    source_type: &str,
    source_id: &str,
    relationship: &str,
    target_type: &str,
    target_id: &str,
    source: &str,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let id = new_id("gedge");
    tx.execute(
        "INSERT INTO graph_edges (id, source_type, source_id, relationship, target_type, target_id, confidence, source)
         VALUES ($1, $2, $3, $4, $5, $6, 1.0, $7)
         ON CONFLICT (source_type, source_id, relationship, target_type, target_id)
         DO UPDATE SET last_seen = NOW()",
        &[&id, &source_type, &source_id, &relationship, &target_type, &target_id, &source],
    )
    .await?;
    Ok(())
}

async fn persist_rdap(
    pool: &Pool,
    domain_id: &str,
    raw: &Value,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let mut client = pool.get().await?;

    let registrar = raw.get("registrar").and_then(|v| v.as_str());
    let registry = raw.get("registry").and_then(|v| v.as_str());
    let created_at = parse_datetime(raw.get("created_at"));
    let updated_at = parse_datetime(raw.get("updated_at"));
    let expires_at = parse_datetime(raw.get("expires_at"));
    let statuses = Json(raw.get("statuses").cloned().unwrap_or(Value::Array(vec![])));
    let nameservers = Json(raw.get("nameservers").cloned().unwrap_or(Value::Array(vec![])));
    let entities = Json(raw.get("entities").cloned().unwrap_or(Value::Array(vec![])));
    let redacted = raw.get("redacted").and_then(|v| v.as_bool()).unwrap_or(false);
    let stored_raw = Json(raw.get("raw").cloned().unwrap_or(raw.clone()));

    let id = new_id("rdap");
    client
        .execute(
            "INSERT INTO rdap_records (id, domain_id, registrar, registry, created_at, updated_at, expires_at,
             statuses, nameservers, entities, redacted, raw_json, fetched_at)
             VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NOW())",
            &[
                &id, &domain_id, &registrar, &registry, &created_at, &updated_at, &expires_at,
                &statuses, &nameservers, &entities, &redacted, &stored_raw,
            ],
        )
        .await?;

    let mut tx = client.transaction().await?;

    if let Some(reg) = registrar {
        upsert_graph_edge(
            &tx, "domain", domain_id, "registered_with", "registrar", reg, "rdap",
        )
        .await?;
    }

    if let Some(ns_list) = raw.get("nameservers").and_then(|v| v.as_array()) {
        for ns in ns_list {
            if let Some(ns_str) = ns.as_str() {
                upsert_graph_edge(
                    &tx, "domain", domain_id, "uses_ns", "nameserver", ns_str, "rdap",
                )
                .await?;
            }
        }
    }

    if let Some(ent_list) = raw.get("entities").and_then(|v| v.as_array()) {
        for ent in ent_list {
            if let Some(handle) = ent.get("handle").and_then(|v| v.as_str()) {
                if !ent.get("redacted").and_then(|v| v.as_bool()).unwrap_or(false) {
                    upsert_graph_edge(
                        &tx, "domain", domain_id, "has_entity", "entity", handle, "rdap",
                    )
                    .await?;
                }
            }
        }
    }

    tx.commit().await?;
    Ok(())
}

async fn persist_dns(
    pool: &Pool,
    domain_id: &str,
    apex: &str,
    raw: &Value,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let records = raw.get("records").and_then(|v| v.as_object());
    let Some(records) = records else {
        return Ok(());
    };

    let host = raw.get("host").and_then(|v| v.as_str()).unwrap_or(apex);
    let mut client = pool.get().await?;
    let mut tx = client.transaction().await?;

    upsert_host_tx(&tx, host, apex).await?;

    for (record_type, values) in records {
        let values = match values.as_array() {
            Some(v) => v,
            None => continue,
        };
        for value in values {
            let value_str = match value.as_str() {
                Some(s) => s.to_string(),
                None => continue,
            };
            let rec_id = new_id("dns");
            tx.execute(
                "INSERT INTO dns_records (id, name, record_type, value, first_seen, last_seen)
                 VALUES ($1, $2, $3, $4, NOW(), NOW())
                 ON CONFLICT (name, record_type, value) DO UPDATE SET last_seen = NOW()",
                &[&rec_id, &host, record_type, &value_str],
            )
            .await?;

            let relationship = match record_type.as_str() {
                "A" | "AAAA" => Some("resolves_to"),
                "NS" => Some("uses_ns"),
                "MX" => Some("uses_mx"),
                "CNAME" => Some("cname_to"),
                _ => None,
            };
            if let Some(rel) = relationship {
                let target_type = match record_type.as_str() {
                    "A" | "AAAA" => "ip",
                    "NS" => "nameserver",
                    "MX" => "mx",
                    "CNAME" => "host",
                    _ => "value",
                };
                upsert_graph_edge(
                    &tx, "domain", domain_id, rel, target_type, &value_str, "dns",
                )
                .await?;
            }
        }
    }

    tx.commit().await?;
    Ok(())
}

async fn persist_ct(
    pool: &Pool,
    domain_id: &str,
    apex: &str,
    raw: &Value,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let mut client = pool.get().await?;
    let mut tx = client.transaction().await?;

    if let Some(names) = raw.get("names").and_then(|v| v.as_array()) {
        for name in names {
            let name_str = match name.as_str() {
                Some(s) => s,
                None => continue,
            };
            upsert_host_tx(&tx, name_str, apex).await?;
            upsert_graph_edge(
                &tx, "domain", domain_id, "has_subdomain", "host", name_str, "ct",
            )
            .await?;
        }
    }

    if let Some(certs) = raw.get("certificates").and_then(|v| v.as_array()) {
        for cert in certs {
            if let Some(fp) = cert.get("fingerprint_sha256").and_then(|v| v.as_str()) {
                upsert_graph_edge(
                    &tx, "domain", domain_id, "has_certificate", "certificate", fp, "ct",
                )
                .await?;
            }
        }
    }

    tx.commit().await?;
    Ok(())
}

async fn persist_http(
    pool: &Pool,
    domain_id: &str,
    apex: &str,
    raw: &Value,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let host = raw
        .get("url")
        .and_then(|v| v.as_str())
        .map(|u| {
            u.trim_start_matches("https://")
                .trim_start_matches("http://")
                .split('/')
                .next()
                .unwrap_or(apex)
                .to_lowercase()
        })
        .unwrap_or_else(|| apex.to_string());

    let mut client = pool.get().await?;
    let host_id = upsert_host(&client, &host, apex).await?;

    let fingerprint_id = new_id("hf");
    let headers = Json(raw.get("headers").cloned().unwrap_or(Value::Object(Default::default())));
    let technologies = Json(Value::Array(vec![]));
    let tracker_ids = Json(raw.get("analytics_ids").cloned().unwrap_or(Value::Array(vec![])));

    client
        .execute(
            "INSERT INTO http_fingerprints (id, host_id, scheme, status_code, title, server_header,
             headers, favicon_hash, technologies, tracker_ids, final_url, fetched_at)
             VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW())",
            &[
                &fingerprint_id,
                &host_id,
                &raw.get("scheme").and_then(|v| v.as_str()).unwrap_or("https"),
                &raw.get("status_code").and_then(|v| v.as_i64()).map(|v| v as i32),
                &raw.get("title").and_then(|v| v.as_str()),
                &raw.get("server").and_then(|v| v.as_str()),
                &headers,
                &raw.get("favicon_hash").and_then(|v| v.as_str()),
                &technologies,
                &tracker_ids,
                &raw.get("final_url").and_then(|v| v.as_str()),
            ],
        )
        .await?;

    if let Some(hash) = raw.get("favicon_hash").and_then(|v| v.as_str()) {
        let tx = client.transaction().await?;
        upsert_graph_edge(
            &tx, "domain", domain_id, "shares_favicon", "favicon_hash", hash, "http",
        )
        .await?;
        tx.commit().await?;
    }

    if let Some(ids) = raw.get("analytics_ids").and_then(|v| v.as_array()) {
        let tx = client.transaction().await?;
        for id in ids {
            if let Some(tracker) = id.as_str() {
                upsert_graph_edge(
                    &tx, "domain", domain_id, "shares_tracker", "tracker_id", tracker, "http",
                )
                .await?;
            }
        }
        tx.commit().await?;
    }

    Ok(())
}

async fn persist_tls(
    pool: &Pool,
    domain_id: &str,
    apex: &str,
    raw: &Value,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let host = raw.get("host").and_then(|v| v.as_str()).unwrap_or(apex);
    let mut client = pool.get().await?;
    let tx = client.transaction().await?;

    upsert_host_tx(&tx, host, apex).await?;

    if let Some(fp) = raw.get("fingerprint").and_then(|v| v.as_str()) {
        let fp = fp.trim_start_matches("sha256:");
        upsert_graph_edge(
            &tx, "domain", domain_id, "has_certificate", "certificate", fp, "tls",
        )
        .await?;
    }

    if let Some(sans) = raw.get("sans").and_then(|v| v.as_array()) {
        for san in sans {
            if let Some(name) = san.as_str() {
                upsert_host_tx(&tx, name, apex).await?;
                upsert_graph_edge(
                    &tx, "domain", domain_id, "has_subdomain", "host", name, "tls",
                )
                .await?;
            }
        }
    }

    if let Some(org) = raw.get("organisation").and_then(|v| v.as_str()) {
        upsert_graph_edge(
            &tx, "domain", domain_id, "registered_with", "organisation", org, "tls",
        )
        .await?;
    }

    tx.commit().await?;
    Ok(())
}

pub async fn sync_campaign_discoveries(
    pool: &Pool,
    job: &CrawlJob,
    result: &CollectorResult,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let apex = registrable_domain(&job.entity_value);
    if apex.is_empty() {
        return Ok(());
    }

    let domain_id = upsert_domain(pool, &apex).await?;
    let source = format!("campaign:{}", job.collector);

    let mut client = pool.get().await?;
    let tx = client.transaction().await?;

    for discovery in &result.discoveries {
        sync_discovery(&tx, &domain_id, &apex, discovery, &source).await?;
    }

    tx.commit().await?;
    Ok(())
}

async fn sync_discovery(
    tx: &tokio_postgres::Transaction<'_>,
    domain_id: &str,
    apex: &str,
    discovery: &DiscoveredEntity,
    source: &str,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let Some((relationship, target_type, target_id)) = map_discovery(discovery, apex) else {
        return Ok(());
    };

    if matches!(discovery.entity_type.as_str(), "domain" | "subdomain") {
        let id = new_id("dom");
        tx.execute(
            "INSERT INTO domains (id, domain, first_seen, last_seen)
             VALUES ($1, $2, NOW(), NOW())
             ON CONFLICT (domain) DO UPDATE SET last_seen = NOW()",
            &[&id, &discovery.value],
        )
        .await?;
    }

    if discovery.entity_type == "subdomain" || discovery.entity_type == "domain" {
        upsert_host_tx(tx, &discovery.value, apex).await?;
    }

    upsert_graph_edge(
        tx,
        "domain",
        domain_id,
        relationship,
        target_type,
        &target_id,
        source,
    )
    .await?;

    Ok(())
}

fn map_discovery(
    discovery: &DiscoveredEntity,
    apex: &str,
) -> Option<(&'static str, &'static str, String)> {
    let value = discovery.value.clone();
    let fp = value.trim_start_matches("sha256:").to_string();

    match (discovery.relation.as_str(), discovery.entity_type.as_str()) {
        ("FOUND_IN_CT", "subdomain" | "domain") => {
            Some(("has_subdomain", "host", value))
        }
        ("FOUND_IN_CT", "certificate") => Some(("has_certificate", "certificate", fp)),
        ("RESOLVES_TO", "ip") => Some(("resolves_to", "ip", value)),
        ("USES_NS", "nameserver") => Some(("uses_ns", "nameserver", value)),
        ("USES_MX", "mx") => Some(("uses_mx", "mx", value)),
        ("CNAME_TO", "domain" | "subdomain") => Some(("cname_to", "host", value)),
        ("HAS_CERT", "certificate") => Some(("has_certificate", "certificate", fp)),
        ("CERT_HAS_SAN", "subdomain" | "domain") => Some(("has_subdomain", "host", value)),
        ("REGISTERED_WITH", "organisation") => Some(("registered_with", "organisation", value)),
        ("REGISTERED_WITH", "registrar") => Some(("registered_with", "registrar", value)),
        ("SHARES_FAVICON", "favicon_hash") => Some(("shares_favicon", "favicon_hash", value)),
        ("SHARES_ANALYTICS_ID", "analytics_id") => {
            Some(("shares_tracker", "tracker_id", value))
        }
        ("LINKED_FROM", "subdomain" | "domain") if registrable_domain(&value) == apex => {
            Some(("has_subdomain", "host", value))
        }
        ("REDIRECTS_TO", "url") => Some(("redirects_to", "url", value)),
        _ => None,
    }
}

async fn upsert_host(pool: &deadpool_postgres::Object, host: &str, apex: &str) -> Result<String, Box<dyn std::error::Error + Send + Sync>> {
    let id = new_id("host");
    let row = pool
        .query_one(
            "INSERT INTO hosts (id, hostname, registered_domain, first_seen, last_seen)
             VALUES ($1, $2, $3, NOW(), NOW())
             ON CONFLICT (hostname) DO UPDATE SET last_seen = NOW()
             RETURNING id",
            &[&id, &host, &apex],
        )
        .await?;
    Ok(row.get(0))
}

async fn upsert_host_tx(
    tx: &tokio_postgres::Transaction<'_>,
    host: &str,
    apex: &str,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let id = new_id("host");
    tx.execute(
        "INSERT INTO hosts (id, hostname, registered_domain, first_seen, last_seen)
         VALUES ($1, $2, $3, NOW(), NOW())
         ON CONFLICT (hostname) DO UPDATE SET last_seen = NOW()",
        &[&id, &host, &apex],
    )
    .await?;
    Ok(())
}

fn parse_datetime(value: Option<&Value>) -> Option<chrono::DateTime<chrono::Utc>> {
    let s = value?.as_str()?;
    chrono::DateTime::parse_from_rfc3339(s)
        .ok()
        .map(|dt| dt.with_timezone(&chrono::Utc))
}

#[allow(dead_code)]
fn classify_host_entity(host: &str) -> &'static str {
    classify_host(host)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::models::DiscoveredEntity;

    #[test]
    fn map_discovery_resolves_ct_and_dns_relations() {
        let apex = "example.com";

        let sub = DiscoveredEntity {
            entity_type: "subdomain".into(),
            value: "api.example.com".into(),
            relation: "FOUND_IN_CT".into(),
        };
        let (rel, target_type, target_id) = map_discovery(&sub, apex).unwrap();
        assert_eq!(rel, "has_subdomain");
        assert_eq!(target_type, "host");
        assert_eq!(target_id, "api.example.com");

        let cert = DiscoveredEntity {
            entity_type: "certificate".into(),
            value: "sha256:abc123".into(),
            relation: "HAS_CERT".into(),
        };
        let (_, _, fp) = map_discovery(&cert, apex).unwrap();
        assert_eq!(fp, "abc123");

        let ns = DiscoveredEntity {
            entity_type: "nameserver".into(),
            value: "ns1.example.net".into(),
            relation: "USES_NS".into(),
        };
        let (rel, target_type, _) = map_discovery(&ns, apex).unwrap();
        assert_eq!(rel, "uses_ns");
        assert_eq!(target_type, "nameserver");
    }

    #[test]
    fn map_discovery_ignores_unrelated_linked_hosts() {
        let other = DiscoveredEntity {
            entity_type: "subdomain".into(),
            value: "api.other.com".into(),
            relation: "LINKED_FROM".into(),
        };
        assert!(map_discovery(&other, "example.com").is_none());
    }

    #[test]
    fn parse_datetime_accepts_rfc3339() {
        let value = serde_json::json!("2024-01-15T12:00:00Z");
        let parsed = parse_datetime(Some(&value));
        assert!(parsed.is_some());
    }
}
