//! Certificate Transparency log ingestion — pulls entries directly from public CT logs.

use base64::engine::general_purpose::STANDARD as BASE64;
use base64::Engine;
use chrono::{DateTime, Utc};
use deadpool_postgres::Pool;
use reqwest::Client;
use serde::Deserialize;
use sha2::{Digest, Sha256};
use std::env;
use std::time::Duration;
use uuid::Uuid;
use x509_parser::prelude::*;

const LOG_LIST_URL: &str = "https://www.gstatic.com/ct/log_list/v3/log_list.json";
const DEFAULT_BATCH_SIZE: u64 = 256;
const DEFAULT_POLL_INTERVAL_SECS: u64 = 30;

#[derive(Debug, Clone, Deserialize)]
pub struct IngestorConfig {
    #[serde(default)]
    pub target_tlds: Vec<String>,
    #[serde(default)]
    pub backfill_mode: bool,
    #[serde(default)]
    pub include_readonly: bool,
    #[serde(default = "default_batches_per_cycle")]
    pub batches_per_cycle: u32,
    #[serde(default = "default_batch_size_i32")]
    pub batch_size: u32,
}

fn default_batches_per_cycle() -> u32 {
    1
}

fn default_batch_size_i32() -> u32 {
    256
}

impl Default for IngestorConfig {
    fn default() -> Self {
        Self {
            target_tlds: vec![
                "com".into(),
                "net".into(),
                "org".into(),
                "io".into(),
                "co.uk".into(),
                "com.au".into(),
            ],
            backfill_mode: false,
            include_readonly: false,
            batches_per_cycle: 1,
            batch_size: 256,
        }
    }
}

#[derive(Debug, Deserialize)]
struct LogList {
    operators: Vec<LogOperator>,
}

#[derive(Debug, Deserialize)]
struct LogOperator {
    logs: Vec<LogInfo>,
}

#[derive(Debug, Deserialize)]
struct LogInfo {
    description: String,
    url: String,
    state: LogState,
}

#[derive(Debug, Deserialize)]
struct LogState {
    #[serde(default)]
    readonly: Option<ReadOnlyState>,
    #[serde(default)]
    qualified: Option<serde_json::Value>,
    #[serde(default)]
    usable: Option<serde_json::Value>,
}

#[derive(Debug, Deserialize)]
struct ReadOnlyState {
    timestamp: String,
}

#[derive(Debug, Deserialize)]
struct SignedTreeHead {
    tree_size: u64,
}

#[derive(Debug, Deserialize)]
struct CTEntry {
    leaf_input: String,
    #[allow(dead_code)]
    extra_data: String,
}

pub struct ParsedCert {
    pub fingerprint_sha256: String,
    pub subject_cn: Option<String>,
    pub issuer: Option<String>,
    pub not_before: Option<DateTime<Utc>>,
    pub not_after: Option<DateTime<Utc>>,
    pub raw_der: Vec<u8>,
    pub names: Vec<(String, bool)>,
}

pub fn create_pool(database_url: &str) -> Pool {
    let mut cfg = deadpool_postgres::Config::new();
    cfg.url = Some(database_url.to_string());
    cfg.create_pool(Some(deadpool_postgres::Runtime::Tokio1), tokio_postgres::NoTls)
        .expect("postgres pool")
}

fn new_id(prefix: &str) -> String {
    format!("{}_{}", prefix, Uuid::new_v4())
}

pub async fn run_ingestor(pool: Pool, client: Client) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    loop {
        let config = load_config(&pool).await.unwrap_or_default();

        if let Err(e) = sync_log_list(&pool, &client).await {
            eprintln!("ct-ingestor: log list sync error: {}", e);
        }

        let logs = list_logs(&pool, &config).await?;
        for log in logs {
            let batches = if config.backfill_mode {
                config.batches_per_cycle.max(1)
            } else {
                1
            };

            for _ in 0..batches {
                match ingest_log_batch(&pool, &client, &log.id, &log.url, &config).await {
                    Ok(more) => {
                        if !more {
                            break;
                        }
                    }
                    Err(e) => {
                        eprintln!("ct-ingestor: {} error: {}", log.url, e);
                        break;
                    }
                }
                tokio::time::sleep(Duration::from_millis(200)).await;
            }

            tokio::time::sleep(Duration::from_millis(300)).await;
        }

        let poll_secs = env::var("CT_POLL_INTERVAL_SECS")
            .ok()
            .and_then(|v| v.parse().ok())
            .unwrap_or(DEFAULT_POLL_INTERVAL_SECS);
        tokio::time::sleep(Duration::from_secs(poll_secs)).await;
    }
}

async fn load_config(pool: &Pool) -> Result<IngestorConfig, Box<dyn std::error::Error + Send + Sync>> {
    let client = pool.get().await?;
    let row = client
        .query_opt("SELECT value FROM ingestor_config WHERE key = 'ct'", &[])
        .await?;

    let Some(row) = row else {
        return Ok(IngestorConfig::default());
    };

    let value: serde_json::Value = row.get(0);
    let config: IngestorConfig = serde_json::from_value(value).unwrap_or_default();
    Ok(config)
}

fn domain_matches_tlds(registered: &str, tlds: &[String]) -> bool {
    if tlds.is_empty() {
        return true;
    }
    let registered = registered.to_lowercase();
    for tld in tlds {
        let tld = tld.trim().trim_start_matches('.').to_lowercase();
        if registered == tld || registered.ends_with(&format!(".{tld}")) {
            return true;
        }
    }
    false
}

fn cert_matches_tlds(cert: &ParsedCert, tlds: &[String]) -> bool {
    if tlds.is_empty() {
        return true;
    }
    for (name, _) in &cert.names {
        let registered = registrable_domain(name);
        if domain_matches_tlds(&registered, tlds) {
            return true;
        }
    }
    false
}

struct ActiveLog {
    id: String,
    url: String,
}

async fn list_logs(pool: &Pool, config: &IngestorConfig) -> Result<Vec<ActiveLog>, Box<dyn std::error::Error + Send + Sync>> {
    let client = pool.get().await?;
    let query = if config.include_readonly {
        "SELECT id, url FROM ct_logs WHERE state IN ('active', 'readonly') ORDER BY state ASC, updated_at ASC"
    } else {
        "SELECT id, url FROM ct_logs WHERE state = 'active' ORDER BY updated_at ASC"
    };
    let rows = client.query(query, &[]).await?;

    Ok(rows
        .iter()
        .map(|r| ActiveLog {
            id: r.get(0),
            url: r.get(1),
        })
        .collect())
}

pub async fn sync_log_list(pool: &Pool, client: &Client) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let response = client
        .get(LOG_LIST_URL)
        .timeout(Duration::from_secs(30))
        .send()
        .await?;

    let list: LogList = response.json().await?;
    let db = pool.get().await?;

    for op in list.operators {
        for log in op.logs {
            let state = if log.state.readonly.is_some() {
                "readonly"
            } else if log.state.qualified.is_some() || log.state.usable.is_some() {
                "active"
            } else {
                "inactive"
            };

            let id = new_id("ctlog");
            db.execute(
                "INSERT INTO ct_logs (id, url, description, state, updated_at)
                 VALUES ($1, $2, $3, $4, NOW())
                 ON CONFLICT (url) DO UPDATE SET
                   description = EXCLUDED.description,
                   state = EXCLUDED.state,
                   updated_at = NOW()",
                &[&id, &log.url, &log.description, &state],
            )
            .await?;
        }
    }

    Ok(())
}

async fn ingest_log_batch(
    pool: &Pool,
    client: &Client,
    log_id: &str,
    log_url: &str,
    config: &IngestorConfig,
) -> Result<bool, Box<dyn std::error::Error + Send + Sync>> {
    let batch_size = config.batch_size.max(1) as u64;
    let base = log_url.trim_end_matches('/');
    let sth_url = format!("{base}/ct/v1/get-sth");
    let sth: SignedTreeHead = client
        .get(&sth_url)
        .timeout(Duration::from_secs(30))
        .send()
        .await?
        .json()
        .await?;

    let db = pool.get().await?;
    let row = db
        .query_one(
            "SELECT last_fetched_index FROM ct_logs WHERE id = $1",
            &[&log_id],
        )
        .await?;
    let mut cursor: i64 = row.get(0);

    if cursor as u64 >= sth.tree_size {
        db.execute(
            "UPDATE ct_logs SET last_tree_size = $2, updated_at = NOW() WHERE id = $1",
            &[&log_id, &(sth.tree_size as i64)],
        )
        .await?;
        return Ok(false);
    }

    let start = cursor as u64;
    let end = std::cmp::min(start + batch_size - 1, sth.tree_size.saturating_sub(1));

    let entries_url = format!("{base}/ct/v1/get-entries?start={start}&end={end}");
    let entries: Vec<CTEntry> = client
        .get(&entries_url)
        .timeout(Duration::from_secs(60))
        .send()
        .await?
        .json()
        .await?;

    let now = Utc::now();
    for entry in entries {
        let leaf = match BASE64.decode(&entry.leaf_input) {
            Ok(v) => v,
            Err(_) => continue,
        };

        let der = match extract_cert_der(&leaf) {
            Some(d) => d,
            None => continue,
        };

        let parsed = match parse_certificate(&der) {
            Some(p) => p,
            None => continue,
        };

        if !cert_matches_tlds(&parsed, &config.target_tlds) {
            continue;
        }

        if let Err(e) = store_certificate(pool, log_id, &parsed, now, &config.target_tlds).await {
            eprintln!("ct-ingestor: store cert error: {}", e);
        }
    }

    cursor = (end + 1) as i64;
    db.execute(
        "UPDATE ct_logs SET last_fetched_index = $2, last_tree_size = $3, updated_at = NOW() WHERE id = $1",
        &[&log_id, &cursor, &(sth.tree_size as i64)],
    )
    .await?;

    println!(
        "ct-ingestor: {} ingested entries {}-{} (tree_size={})",
        log_url, start, end, sth.tree_size
    );

    let has_more = cursor < sth.tree_size as i64;
    Ok(has_more)
}

fn extract_cert_der(leaf_input: &[u8]) -> Option<Vec<u8>> {
    if leaf_input.len() < 12 {
        return None;
    }
    if leaf_input[0] != 0 || leaf_input[1] != 0 {
        return None;
    }

    let entry_type = u16::from_be_bytes([leaf_input[10], leaf_input[11]]);
    match entry_type {
        0 => read_length_prefixed(&leaf_input[12..]),
        1 => {
            if leaf_input.len() < 12 + 64 + 3 {
                return None;
            }
            read_length_prefixed(&leaf_input[12 + 64..])
        }
        _ => None,
    }
}

fn read_length_prefixed(data: &[u8]) -> Option<Vec<u8>> {
    if data.len() < 3 {
        return None;
    }
    let len = ((data[0] as usize) << 16) | ((data[1] as usize) << 8) | (data[2] as usize);
    if data.len() < 3 + len {
        return None;
    }
    Some(data[3..3 + len].to_vec())
}

pub fn parse_certificate(der: &[u8]) -> Option<ParsedCert> {
    let (_, cert) = X509Certificate::from_der(der).ok()?;
    let fingerprint = hex::encode(Sha256::digest(der));

    let subject_cn = cert
        .subject()
        .iter_common_name()
        .next()
        .and_then(|cn| cn.as_str().ok())
        .map(|s| s.to_string());

    let issuer = cert
        .issuer()
        .iter_common_name()
        .next()
        .and_then(|cn| cn.as_str().ok())
        .map(|s| s.to_string());

    let not_before = chrono::DateTime::parse_from_rfc2822(&cert.validity().not_before.to_string())
        .ok()
        .map(|dt| dt.with_timezone(&chrono::Utc));

    let not_after = chrono::DateTime::parse_from_rfc2822(&cert.validity().not_after.to_string())
        .ok()
        .map(|dt| dt.with_timezone(&chrono::Utc));

    let mut names = Vec::new();
    if let Some(cn) = &subject_cn {
        let wildcard = cn.starts_with("*.");
        names.push((cn.trim_start_matches("*.").to_lowercase(), wildcard));
    }

    if let Ok(san) = cert.subject_alternative_name() {
        if let Some(san_ext) = san {
            for name in san_ext.value.general_names.iter() {
                if let GeneralName::DNSName(dns) = name {
                    let dns = dns.to_string().trim_end_matches('.').to_lowercase();
                    let wildcard = dns.starts_with("*.");
                    names.push((dns.trim_start_matches("*.").to_string(), wildcard));
                }
            }
        }
    }

    Some(ParsedCert {
        fingerprint_sha256: fingerprint,
        subject_cn,
        issuer,
        not_before,
        not_after,
        raw_der: der.to_vec(),
        names,
    })
}

async fn store_certificate(
    pool: &Pool,
    log_id: &str,
    cert: &ParsedCert,
    first_seen: DateTime<Utc>,
    target_tlds: &[String],
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let client = pool.get().await?;
    let cert_id = new_id("cert");

    let row = client
        .query_opt(
            "INSERT INTO certificates (id, fingerprint_sha256, subject_cn, issuer, not_before, not_after, raw_der, first_seen, source_log_id)
             VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
             ON CONFLICT (fingerprint_sha256) DO UPDATE SET source_log_id = COALESCE(certificates.source_log_id, EXCLUDED.source_log_id)
             RETURNING id",
            &[
                &cert_id,
                &cert.fingerprint_sha256,
                &cert.subject_cn,
                &cert.issuer,
                &cert.not_before,
                &cert.not_after,
                &cert.raw_der,
                &first_seen,
                &log_id,
            ],
        )
        .await?;

    let cert_id: String = if let Some(r) = row {
        r.get(0)
    } else {
        let r = client
            .query_one(
                "SELECT id FROM certificates WHERE fingerprint_sha256 = $1",
                &[&cert.fingerprint_sha256],
            )
            .await?;
        r.get(0)
    };

    for (name, is_wildcard) in &cert.names {
        if name.is_empty() || name.contains(' ') {
            continue;
        }
        let registered = registrable_domain(name);
        if registered.is_empty() {
            continue;
        }
        if !domain_matches_tlds(&registered, target_tlds) {
            continue;
        }

        let name_id = new_id("cn");
        client
            .execute(
                "INSERT INTO certificate_names (id, certificate_id, name, registered_domain, is_wildcard, first_seen)
                 VALUES ($1, $2, $3, $4, $5, $6)
                 ON CONFLICT (certificate_id, name) DO NOTHING",
                &[&name_id, &cert_id, name, &registered, is_wildcard, &first_seen],
            )
            .await?;

        let dom_id = new_id("dom");
        client
            .execute(
                "INSERT INTO domains (id, domain, first_seen, last_seen)
                 VALUES ($1, $2, $3, $3)
                 ON CONFLICT (domain) DO UPDATE SET last_seen = GREATEST(domains.last_seen, EXCLUDED.last_seen)",
                &[&dom_id, &registered, &first_seen],
            )
            .await?;
    }

    Ok(())
}

mod hex {
    pub fn encode(bytes: impl AsRef<[u8]>) -> String {
        bytes.as_ref().iter().map(|b| format!("{:02x}", b)).collect()
    }
}

fn registrable_domain(host: &str) -> String {
    let host = host.trim().trim_start_matches("*.").to_lowercase();
    let parts: Vec<&str> = host.split('.').collect();
    let multi = ["co.uk", "org.uk", "com.au", "co.nz", "co.jp"];
    if parts.len() >= 3 {
        let suffix = format!("{}.{}", parts[parts.len() - 2], parts[parts.len() - 1]);
        if multi.contains(&suffix.as_str()) {
            return format!("{}.{}", parts[parts.len() - 3], suffix);
        }
    }
    if parts.len() >= 2 {
        return format!("{}.{}", parts[parts.len() - 2], parts[parts.len() - 1]);
    }
    host
}

pub fn batch_size() -> u64 {
    env::var("CT_BATCH_SIZE")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(DEFAULT_BATCH_SIZE)
}

pub fn poll_interval() -> Duration {
    let secs: u64 = env::var("CT_POLL_INTERVAL_SECS")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(DEFAULT_POLL_INTERVAL_SECS);
    Duration::from_secs(secs)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn domain_matches_tlds_filters_suffixes() {
        let tlds = vec!["com".into(), "co.uk".into()];
        assert!(domain_matches_tlds("example.com", &tlds));
        assert!(domain_matches_tlds("shop.example.co.uk", &tlds));
        assert!(!domain_matches_tlds("example.io", &tlds));
        assert!(domain_matches_tlds("example.com", &[]));
    }

    #[test]
    fn registrable_domain_handles_uk_suffix() {
        assert_eq!(registrable_domain("api.shop.example.co.uk"), "example.co.uk");
        assert_eq!(registrable_domain("cdn.example.com"), "example.com");
    }

    #[test]
    fn read_length_prefixed_parses_der_chunk() {
        let der = vec![0x30, 0x03, 0x01, 0x02, 0x03];
        let mut buf = vec![0, 0, 5];
        buf.extend_from_slice(&der);
        assert_eq!(read_length_prefixed(&buf), Some(der));
        assert!(read_length_prefixed(&[0, 0]).is_none());
    }

    #[test]
    fn extract_cert_der_rejects_invalid_leaf() {
        assert!(extract_cert_der(&[]).is_none());
        assert!(extract_cert_der(&[1, 0]).is_none());
    }

    #[test]
    fn ingestor_config_defaults_are_valid() {
        let cfg = IngestorConfig::default();
        assert!(!cfg.target_tlds.is_empty());
        assert!(cfg.batch_size > 0);
    }
}
