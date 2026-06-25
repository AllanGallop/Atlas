use crate::collectors::domain::registrable_domain;
use crate::models::{CollectorResult, CrawlJob, DiscoveredEntity};
use deadpool_postgres::Pool;
use serde_json::json;
use std::collections::HashSet;

pub async fn collect_from_store(
    pool: &Pool,
    job: &CrawlJob,
) -> Result<CollectorResult, Box<dyn std::error::Error + Send + Sync>> {
    let apex = registrable_domain(&job.entity_value);
    if apex.is_empty() {
        return Err("empty apex domain".into());
    }

    let client = pool.get().await?;
    let rows = client
        .query(
            "SELECT DISTINCT cn.name, c.fingerprint_sha256, c.subject_cn, c.issuer,
                    c.not_before, c.not_after, c.first_seen
             FROM certificate_names cn
             JOIN certificates c ON c.id = cn.certificate_id
             WHERE cn.registered_domain = $1
             ORDER BY cn.name ASC",
            &[&apex],
        )
        .await?;

    let mut names = HashSet::new();
    let mut discoveries = Vec::new();
    let mut certificates = Vec::new();
    let mut cert_fps = HashSet::new();

    for row in rows {
        let name: String = row.get(0);
        let fingerprint: String = row.get(1);

        if name != apex && names.insert(name.clone()) {
            discoveries.push(DiscoveredEntity {
                entity_type: if name == registrable_domain(&name) {
                    "domain".into()
                } else {
                    "subdomain".into()
                },
                value: name,
                relation: "FOUND_IN_CT".into(),
            });
        }

        if cert_fps.insert(fingerprint.clone()) {
            discoveries.push(DiscoveredEntity {
                entity_type: "certificate".into(),
                value: fingerprint.clone(),
                relation: "FOUND_IN_CT".into(),
            });

            certificates.push(json!({
                "fingerprint_sha256": fingerprint,
                "subject_cn": row.get::<_, Option<String>>(2),
                "issuer": row.get::<_, Option<String>>(3),
                "not_before": row.get::<_, Option<chrono::DateTime<chrono::Utc>>>(4),
                "not_after": row.get::<_, Option<chrono::DateTime<chrono::Utc>>>(5),
                "first_seen": row.get::<_, chrono::DateTime<chrono::Utc>>(6),
            }));
        }
    }

    let discovered_names: Vec<String> = discoveries
        .iter()
        .filter(|d| d.entity_type != "certificate")
        .map(|d| d.value.clone())
        .collect();

    Ok(CollectorResult {
        raw: json!({
            "apex": apex,
            "source": "atlas_ct_store",
            "entry_count": certificates.len(),
            "names": discovered_names,
            "name_count": names.len(),
            "certificates": certificates,
        }),
        discoveries,
        source: "ct.collector".into(),
    })
}
