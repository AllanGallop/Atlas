use crate::collectors::domain::{classify_host, is_crawlable_host, normalize_host as norm_host};
use crate::models::{CollectorResult, CrawlJob, DiscoveredEntity};
use rustls::pki_types::ServerName;
use serde_json::json;
use sha2::{Digest, Sha256};
use std::sync::Arc;
use tokio::net::TcpStream;
use tokio_rustls::TlsConnector;
use x509_parser::prelude::*;

pub async fn collect(job: &CrawlJob) -> Result<CollectorResult, Box<dyn std::error::Error + Send + Sync>> {
    let host = normalize_host(&job.entity_value, &job.entity_type)?;
    let cert_data = fetch_peer_certificate(&host).await?;
    let parsed = parse_x509_certificate(&cert_data)?;
    let cert = parsed.1;

    let fingerprint = format!("sha256:{:x}", Sha256::digest(&cert_data));
    let subject = cert.subject().to_string();
    let issuer = cert.issuer().to_string();
    let not_before = cert.validity().not_before.to_string();
    let not_after = cert.validity().not_after.to_string();

    let mut org = None;
    for rdn in cert.subject().iter_common_name() {
        if let Ok(value) = rdn.as_str() {
            org = Some(value.to_string());
            break;
        }
    }
    for rdn in cert.subject().iter_organization() {
        if let Ok(value) = rdn.as_str() {
            org = Some(value.to_string());
            break;
        }
    }

    let mut sans = Vec::new();
    if let Ok(Some(ext)) = cert.subject_alternative_name() {
        for name in &ext.value.general_names {
            if let GeneralName::DNSName(dns) = name {
                let value = dns.to_string().trim_end_matches('.').to_lowercase();
                if !value.is_empty() && value != "*" {
                    sans.push(value);
                }
            }
        }
    }

    let mut discoveries = Vec::new();
    discoveries.push(DiscoveredEntity {
        entity_type: "certificate".into(),
        value: fingerprint.clone(),
        relation: "HAS_CERT".into(),
    });

    if let Some(organisation) = org.clone() {
        discoveries.push(DiscoveredEntity {
            entity_type: "organisation".into(),
            value: organisation,
            relation: "REGISTERED_WITH".into(),
        });
    }

    for san in &sans {
        if !is_crawlable_host(san) {
            continue;
        }
        discoveries.push(DiscoveredEntity {
            entity_type: classify_host(san).to_string(),
            value: san.clone(),
            relation: "CERT_HAS_SAN".into(),
        });
    }

    Ok(CollectorResult {
        raw: json!({
            "host": host,
            "fingerprint": fingerprint,
            "subject": subject,
            "issuer": issuer,
            "organisation": org,
            "not_before": not_before,
            "not_after": not_after,
            "sans": sans,
        }),
        discoveries,
        source: "tls.collector".into(),
    })
}

fn normalize_host(value: &str, entity_type: &str) -> Result<String, Box<dyn std::error::Error + Send + Sync>> {
    let host = norm_host(value);
    if host.is_empty() {
        return Err("empty host".into());
    }
    if entity_type == "ip" {
        return Ok(host);
    }
    Ok(host)
}

async fn fetch_peer_certificate(host: &str) -> Result<Vec<u8>, Box<dyn std::error::Error + Send + Sync>> {
    let addr = format!("{}:443", host);
    let tcp = TcpStream::connect(&addr).await?;
    tcp.set_nodelay(true)?;

    let mut root_store = rustls::RootCertStore::empty();
    root_store.extend(webpki_roots::TLS_SERVER_ROOTS.iter().cloned());

    let config = rustls::ClientConfig::builder()
        .with_root_certificates(root_store)
        .with_no_client_auth();

    let connector = TlsConnector::from(Arc::new(config));
    let server_name = ServerName::try_from(host.to_string()).map_err(|e| format!("invalid sni: {}", e))?;
    let tls = connector.connect(server_name, tcp).await?;

    let (_, session) = tls.get_ref();
    let certs = session.peer_certificates().ok_or("no peer certificates")?;
    let leaf = certs.first().ok_or("empty certificate chain")?;
    Ok(leaf.as_ref().to_vec())
}
