use crate::collectors::domain::registrable_domain;
use crate::models::{CollectorResult, CrawlJob, DiscoveredEntity};
use deadpool_postgres::Pool;
use reqwest::Client;
use serde::Deserialize;
use serde_json::{json, Value};
use std::time::Duration;

const IANA_BOOTSTRAP: &str = "https://data.iana.org/rdap/dns.json";
const CACHE_TTL_HOURS: i64 = 168; // 7 days

#[derive(Debug, Deserialize)]
struct BootstrapFile {
    services: Vec<Vec<Value>>,
}

#[derive(Debug, Deserialize)]
struct RDAPResponse {
    #[serde(default)]
    ldh_name: Option<String>,
    #[serde(default)]
    status: Vec<String>,
    #[serde(default)]
    events: Vec<RDAPEvent>,
    #[serde(default)]
    nameservers: Vec<RDAPNameserver>,
    #[serde(default)]
    entities: Vec<RDAPEntity>,
    #[serde(default)]
    secure_dns: Option<RDAPSecureDNS>,
    #[serde(default)]
    port43: Option<String>,
}

#[derive(Debug, Deserialize)]
struct RDAPEvent {
    event_action: String,
    event_date: String,
}

#[derive(Debug, Deserialize)]
struct RDAPNameserver {
    #[serde(default)]
    ldh_name: Option<String>,
    #[serde(default)]
    unicode_name: Option<String>,
}

#[derive(Debug, Deserialize)]
struct RDAPEntity {
    #[serde(default)]
    handle: Option<String>,
    #[serde(default)]
    roles: Vec<String>,
    #[serde(default)]
    vcard_array: Option<Value>,
    #[serde(default)]
    remarks: Vec<RDAPRemark>,
    #[serde(default)]
    object_class_name: Option<String>,
}

#[derive(Debug, Deserialize)]
struct RDAPRemark {
    #[serde(default)]
    title: Option<String>,
}

#[derive(Debug, Deserialize)]
struct RDAPSecureDNS {
    #[serde(default)]
    delegation_signed: Option<bool>,
}

pub async fn collect(
    client: &Client,
    pool: &Pool,
    job: &CrawlJob,
) -> Result<CollectorResult, Box<dyn std::error::Error + Send + Sync>> {
    let domain = registrable_domain(&job.entity_value);
    if domain.is_empty() {
        return Err("empty domain".into());
    }

    if let Some(cached) = get_cached_rdap(pool, &domain).await? {
        return Ok(cached);
    }

    let rdap_url = resolve_rdap_url(client, &domain).await?;
    tokio::time::sleep(Duration::from_millis(250)).await;

    let response = client
        .get(&rdap_url)
        .header("Accept", "application/rdap+json, application/json")
        .header("User-Agent", "AtlasDiscovery/0.1")
        .timeout(Duration::from_secs(30))
        .send()
        .await?;

    if response.status().as_u16() == 429 {
        return Err("RDAP rate limited (429)".into());
    }
    if !response.status().is_success() {
        return Err(format!("RDAP returned {}", response.status()).into());
    }

    let raw: Value = response.json().await?;
    let rdap: RDAPResponse = serde_json::from_value(raw.clone())?;

    let registrar = find_entity_org(&rdap.entities, &["registrar"]);
    let registry = find_entity_org(&rdap.entities, &["registry", "technical"]);
    let created_at = find_event_date(&rdap.events, "registration");
    let updated_at = find_event_date(&rdap.events, "last changed");
    let expires_at = find_event_date(&rdap.events, "expiration");

    let nameservers: Vec<String> = rdap
        .nameservers
        .iter()
        .filter_map(|ns| {
            ns.ldh_name
                .as_ref()
                .or(ns.unicode_name.as_ref())
                .map(|n| n.trim_end_matches('.').to_lowercase())
        })
        .collect();

    let redacted = is_redacted(&rdap.entities);
    let entities_json = normalize_entities(&rdap.entities);
    let dnssec = rdap.secure_dns.as_ref().and_then(|s| s.delegation_signed);

    let mut discoveries = Vec::new();
    for ns in &nameservers {
        discoveries.push(DiscoveredEntity {
            entity_type: "nameserver".into(),
            value: ns.clone(),
            relation: "USES_NS".into(),
        });
    }

    if let Some(reg) = &registrar {
        discoveries.push(DiscoveredEntity {
            entity_type: "registrar".into(),
            value: reg.clone(),
            relation: "REGISTERED_WITH".into(),
        });
    }

    Ok(CollectorResult {
        raw: json!({
            "domain": domain,
            "source": "rdap",
            "rdap_url": rdap_url,
            "registrar": registrar,
            "registry": registry,
            "created_at": created_at,
            "updated_at": updated_at,
            "expires_at": expires_at,
            "statuses": rdap.status,
            "nameservers": nameservers,
            "entities": entities_json,
            "redacted": redacted,
            "dnssec": dnssec,
            "raw": raw,
        }),
        discoveries,
        source: "rdap.collector".into(),
    })
}

async fn get_cached_rdap(
    pool: &Pool,
    domain: &str,
) -> Result<Option<CollectorResult>, Box<dyn std::error::Error + Send + Sync>> {
    let client = pool.get().await?;
    let row = client
        .query_opt(
            "SELECT r.registrar, r.registry, r.created_at, r.updated_at, r.expires_at,
                    r.statuses, r.nameservers, r.entities, r.redacted, r.raw_json, r.fetched_at
             FROM rdap_records r
             JOIN domains d ON d.id = r.domain_id
             WHERE d.domain = $1
             ORDER BY r.fetched_at DESC
             LIMIT 1",
            &[&domain],
        )
        .await?;

    let Some(row) = row else {
        return Ok(None);
    };

    let fetched_at: chrono::DateTime<chrono::Utc> = row.get(10);
    let age = chrono::Utc::now().signed_duration_since(fetched_at);
    if age.num_hours() > CACHE_TTL_HOURS {
        return Ok(None);
    }

    let raw = json!({
        "domain": domain,
        "source": "rdap",
        "registrar": row.get::<_, Option<String>>(0),
        "registry": row.get::<_, Option<String>>(1),
        "created_at": row.get::<_, Option<chrono::DateTime<chrono::Utc>>>(2),
        "updated_at": row.get::<_, Option<chrono::DateTime<chrono::Utc>>>(3),
        "expires_at": row.get::<_, Option<chrono::DateTime<chrono::Utc>>>(4),
        "statuses": row.get::<_, serde_json::Value>(5),
        "nameservers": row.get::<_, serde_json::Value>(6),
        "entities": row.get::<_, serde_json::Value>(7),
        "redacted": row.get::<_, bool>(8),
        "raw": row.get::<_, serde_json::Value>(9),
    });

    Ok(Some(CollectorResult {
        raw,
        discoveries: vec![],
        source: "rdap.cache".into(),
    }))
}

async fn resolve_rdap_url(client: &Client, domain: &str) -> Result<String, Box<dyn std::error::Error + Send + Sync>> {
    let bootstrap: BootstrapFile = client
        .get(IANA_BOOTSTRAP)
        .timeout(Duration::from_secs(15))
        .send()
        .await?
        .json()
        .await?;

    let tld = domain.rsplit('.').next().unwrap_or(domain);

    for service in bootstrap.services {
        if service.len() < 2 {
            continue;
        }
        let tlds: Vec<String> = serde_json::from_value(service[0].clone()).unwrap_or_default();
        if !tlds.iter().any(|t| t == tld) {
            continue;
        }
        let urls: Vec<String> = serde_json::from_value(service[1].clone()).unwrap_or_default();
        if let Some(base) = urls.first() {
            let base = base.trim_end_matches('/');
            return Ok(format!("{base}/domain/{domain}"));
        }
    }

    Err(format!("no RDAP server for TLD .{}", tld).into())
}

fn find_entity_org(entities: &[RDAPEntity], roles: &[&str]) -> Option<String> {
    for entity in entities {
        if !entity.roles.iter().any(|r| roles.contains(&r.as_str())) {
            continue;
        }
        if let Some(org) = vcard_org(entity) {
            return Some(org);
        }
        if let Some(handle) = &entity.handle {
            return Some(handle.clone());
        }
    }
    None
}

fn vcard_org(entity: &RDAPEntity) -> Option<String> {
    let vcard = entity.vcard_array.as_ref()?;
    let items = vcard.get(1)?.as_array()?;
    for item in items {
        let arr = item.as_array()?;
        if arr.first()?.as_str()? == "fn" {
            return arr.get(3)?.as_str().map(|s| s.to_string());
        }
        if arr.first()?.as_str()? == "org" {
            return arr.get(3)?.as_str().map(|s| s.to_string());
        }
    }
    None
}

fn find_event_date(events: &[RDAPEvent], action: &str) -> Option<String> {
    events
        .iter()
        .find(|e| e.event_action == action)
        .map(|e| e.event_date.clone())
}

fn is_redacted(entities: &[RDAPEntity]) -> bool {
    entities.iter().any(|e| {
        e.remarks.iter().any(|r| {
            r.title
                .as_ref()
                .map(|t| t.to_lowercase().contains("redact"))
                .unwrap_or(false)
        })
    })
}

fn normalize_entities(entities: &[RDAPEntity]) -> Vec<Value> {
    entities
        .iter()
        .filter_map(|e| {
            let org = vcard_org(e);
            if e.handle.is_none() && org.is_none() {
                return None;
            }
            Some(json!({
                "handle": e.handle,
                "roles": e.roles,
                "org": org,
                "redacted": e.remarks.iter().any(|r| {
                    r.title.as_ref().map(|t| t.to_lowercase().contains("redact")).unwrap_or(false)
                }),
            }))
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn find_event_date_matches_action() {
        let events = vec![
            RDAPEvent {
                event_action: "registration".into(),
                event_date: "2019-01-01T00:00:00Z".into(),
            },
            RDAPEvent {
                event_action: "expiration".into(),
                event_date: "2030-01-01T00:00:00Z".into(),
            },
        ];
        assert_eq!(
            find_event_date(&events, "registration").as_deref(),
            Some("2019-01-01T00:00:00Z")
        );
        assert!(find_event_date(&events, "last changed").is_none());
    }

    #[test]
    fn is_redacted_detects_remarks() {
        let entities = vec![RDAPEntity {
            handle: Some("HIDDEN".into()),
            roles: vec![],
            vcard_array: None,
            remarks: vec![RDAPRemark {
                title: Some("Redacted for privacy".into()),
            }],
            object_class_name: None,
        }];
        assert!(is_redacted(&entities));
    }

    #[test]
    fn normalize_entities_skips_empty_records() {
        let entities = vec![
            RDAPEntity {
                handle: Some("EXAMPLE-REG".into()),
                roles: vec!["registrant".into()],
                vcard_array: Some(json!(["vcard", [["fn", {}, "text", "Example Org"]]])),
                remarks: vec![],
                object_class_name: None,
            },
            RDAPEntity {
                handle: None,
                roles: vec![],
                vcard_array: None,
                remarks: vec![],
                object_class_name: None,
            },
        ];
        let out = normalize_entities(&entities);
        assert_eq!(out.len(), 1);
        assert_eq!(out[0]["handle"], "EXAMPLE-REG");
        assert_eq!(out[0]["org"], "Example Org");
    }
}
