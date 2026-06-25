use crate::collectors::domain::classify_host;
use crate::models::{CollectorResult, CrawlJob, DiscoveredEntity};
use hickory_resolver::config::{ResolverConfig, ResolverOpts};
use hickory_resolver::proto::rr::record_data::RData;
use hickory_resolver::proto::rr::RecordType;
use hickory_resolver::TokioAsyncResolver;
use serde_json::json;

pub fn create_resolver() -> TokioAsyncResolver {
    TokioAsyncResolver::tokio(ResolverConfig::default(), ResolverOpts::default())
}

pub async fn collect(
    resolver: &TokioAsyncResolver,
    job: &CrawlJob,
) -> Result<CollectorResult, Box<dyn std::error::Error + Send + Sync>> {
    let host = normalize_host(&job.entity_value, &job.entity_type)?;
    let mut discoveries = Vec::new();
    let mut records = serde_json::Map::new();

    lookup_ipv4(resolver, &host, &mut discoveries, &mut records).await;
    lookup_ipv6(resolver, &host, &mut discoveries, &mut records).await;
    lookup_txt(resolver, &host, &mut discoveries, &mut records).await;
    lookup_cname(resolver, &host, &mut discoveries, &mut records).await;
    lookup_ns(resolver, &host, &mut discoveries, &mut records).await;
    lookup_mx(resolver, &host, &mut discoveries, &mut records).await;

    Ok(CollectorResult {
        raw: json!({
            "host": host,
            "records": records,
        }),
        discoveries,
        source: "dns.collector".into(),
    })
}

fn normalize_host(value: &str, entity_type: &str) -> Result<String, Box<dyn std::error::Error + Send + Sync>> {
    let host = value.trim().trim_end_matches('.').to_lowercase();
    if host.is_empty() {
        return Err("empty host".into());
    }
    if entity_type == "ip" {
        return Err("dns collector does not accept ip seeds".into());
    }
    Ok(host)
}

async fn lookup_ipv4(
    resolver: &TokioAsyncResolver,
    host: &str,
    discoveries: &mut Vec<DiscoveredEntity>,
    records: &mut serde_json::Map<String, serde_json::Value>,
) {
    match resolver.ipv4_lookup(host).await {
        Ok(lookup) => {
            let values: Vec<String> = lookup.iter().map(|r| r.to_string()).collect();
            records.insert("A".into(), json!(values));
            for ip in values {
                discoveries.push(DiscoveredEntity {
                    entity_type: "ip".into(),
                    value: ip,
                    relation: "RESOLVES_TO".into(),
                });
            }
        }
        Err(_) => {
            records.insert("A".into(), json!([]));
        }
    }
}

async fn lookup_ipv6(
    resolver: &TokioAsyncResolver,
    host: &str,
    discoveries: &mut Vec<DiscoveredEntity>,
    records: &mut serde_json::Map<String, serde_json::Value>,
) {
    match resolver.ipv6_lookup(host).await {
        Ok(lookup) => {
            let values: Vec<String> = lookup.iter().map(|r| r.to_string()).collect();
            records.insert("AAAA".into(), json!(values));
            for ip in values {
                discoveries.push(DiscoveredEntity {
                    entity_type: "ip".into(),
                    value: ip,
                    relation: "RESOLVES_TO".into(),
                });
            }
        }
        Err(_) => {
            records.insert("AAAA".into(), json!([]));
        }
    }
}

async fn lookup_txt(
    resolver: &TokioAsyncResolver,
    host: &str,
    discoveries: &mut Vec<DiscoveredEntity>,
    records: &mut serde_json::Map<String, serde_json::Value>,
) {
    match resolver.txt_lookup(host).await {
        Ok(lookup) => {
            let values: Vec<String> = lookup
                .iter()
                .flat_map(|txt| {
                    txt.iter()
                        .map(|s| String::from_utf8_lossy(s).to_string())
                        .collect::<Vec<_>>()
                })
                .collect();
            records.insert("TXT".into(), json!(values));
            for txt in values {
                discoveries.push(DiscoveredEntity {
                    entity_type: "txt".into(),
                    value: txt,
                    relation: "HAS_TXT".into(),
                });
            }
        }
        Err(_) => {
            records.insert("TXT".into(), json!([]));
        }
    }
}

async fn lookup_cname(
    resolver: &TokioAsyncResolver,
    host: &str,
    discoveries: &mut Vec<DiscoveredEntity>,
    records: &mut serde_json::Map<String, serde_json::Value>,
) {
    match resolver.lookup(host, RecordType::CNAME).await {
        Ok(lookup) => {
            let mut values = Vec::new();
            for record in lookup.record_iter() {
                if let Some(RData::CNAME(cname)) = record.data() {
                    let target = cname.to_string().trim_end_matches('.').to_lowercase();
                    values.push(target);
                }
            }
            records.insert("CNAME".into(), json!(values));
            for target in values {
                let entity_type = classify_host(&target).to_string();
                discoveries.push(DiscoveredEntity {
                    entity_type,
                    value: target,
                    relation: "CNAME_TO".into(),
                });
            }
        }
        Err(_) => {
            records.insert("CNAME".into(), json!([]));
        }
    }
}

async fn lookup_ns(
    resolver: &TokioAsyncResolver,
    host: &str,
    discoveries: &mut Vec<DiscoveredEntity>,
    records: &mut serde_json::Map<String, serde_json::Value>,
) {
    match resolver.ns_lookup(host).await {
        Ok(lookup) => {
            let values: Vec<String> = lookup
                .iter()
                .map(|r| r.to_string().trim_end_matches('.').to_lowercase())
                .collect();
            records.insert("NS".into(), json!(values));
            for ns in values {
                discoveries.push(DiscoveredEntity {
                    entity_type: "nameserver".into(),
                    value: ns,
                    relation: "USES_NS".into(),
                });
            }
        }
        Err(_) => {
            records.insert("NS".into(), json!([]));
        }
    }
}

async fn lookup_mx(
    resolver: &TokioAsyncResolver,
    host: &str,
    discoveries: &mut Vec<DiscoveredEntity>,
    records: &mut serde_json::Map<String, serde_json::Value>,
) {
    match resolver.mx_lookup(host).await {
        Ok(lookup) => {
            let values: Vec<serde_json::Value> = lookup
                .iter()
                .map(|mx| {
                    json!({
                        "priority": mx.preference(),
                        "host": mx.exchange().to_string().trim_end_matches('.').to_lowercase(),
                    })
                })
                .collect();
            records.insert("MX".into(), json!(values));
            for mx in lookup.iter() {
                let host = mx.exchange().to_string().trim_end_matches('.').to_lowercase();
                if host.is_empty() || host == "." {
                    continue;
                }
                discoveries.push(DiscoveredEntity {
                    entity_type: "mx".into(),
                    value: host,
                    relation: "USES_MX".into(),
                });
            }
        }
        Err(_) => {
            records.insert("MX".into(), json!([]));
        }
    }
}
