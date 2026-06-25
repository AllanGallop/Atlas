use crate::collectors::domain::{is_crawlable_host, is_subdomain_of, normalize_host, registrable_domain};
use crate::models::{CollectorResult, CrawlJob, DiscoveredEntity};
use regex::Regex;
use reqwest::Client;
use serde_json::json;
use sha2::{Digest, Sha256};
use std::collections::{HashMap, HashSet};

pub async fn collect(
    client: &Client,
    job: &CrawlJob,
) -> Result<CollectorResult, Box<dyn std::error::Error + Send + Sync>> {
    let apex = registrable_domain(&job.entity_value);
    let (url, response) = fetch_with_fallback(client, &job.entity_value, &job.entity_type).await?;
    let status = response.status().as_u16();
    let final_url = response.url().to_string();

    let mut headers = HashMap::new();
    for (name, value) in response.headers() {
        headers.insert(
            name.as_str().to_ascii_lowercase(),
            value.to_str().unwrap_or("").to_string(),
        );
    }

    let body = response.text().await.unwrap_or_default();
    let title = extract_title(&body);
    let server = headers.get("server").cloned();
    let analytics = extract_analytics_ids(&body);
    let linked_hosts = extract_linked_hosts(&body, &apex);

    let mut discoveries = Vec::new();

    if final_url != url {
        discoveries.push(DiscoveredEntity {
            entity_type: "url".into(),
            value: final_url.clone(),
            relation: "REDIRECTS_TO".into(),
        });
    }

    let favicon_hash = fetch_favicon_hash(client, &url).await;

    for id in &analytics {
        discoveries.push(DiscoveredEntity {
            entity_type: "analytics_id".into(),
            value: id.clone(),
            relation: "SHARES_ANALYTICS_ID".into(),
        });
    }

    if let Some(ref hash) = favicon_hash {
        discoveries.push(DiscoveredEntity {
            entity_type: "favicon_hash".into(),
            value: hash.clone(),
            relation: "SHARES_FAVICON".into(),
        });
    }

    for host in linked_hosts {
        discoveries.push(DiscoveredEntity {
            entity_type: "subdomain".into(),
            value: host.clone(),
            relation: "LINKED_FROM".into(),
        });
        discoveries.push(DiscoveredEntity {
            entity_type: "url".into(),
            value: format!("http://{host}/"),
            relation: "LINKED_FROM".into(),
        });
    }

    Ok(CollectorResult {
        raw: json!({
            "url": url,
            "final_url": final_url,
            "status_code": status,
            "title": title,
            "server": server,
            "headers": headers,
            "analytics_ids": analytics,
            "favicon_hash": favicon_hash,
            "linked_hosts": discoveries.iter().filter(|d| d.entity_type == "subdomain").map(|d| &d.value).collect::<Vec<_>>(),
            "redirect_chain": if final_url != url { vec![final_url.clone()] } else { vec![] },
        }),
        discoveries,
        source: "http.collector".into(),
    })
}

fn url_candidates(value: &str, entity_type: &str) -> Result<Vec<String>, Box<dyn std::error::Error + Send + Sync>> {
    let trimmed = value.trim();
    if trimmed.is_empty() {
        return Err("empty url".into());
    }
    if entity_type == "ip" && !trimmed.contains("://") {
        return Ok(vec![format!("http://{}", trimmed)]);
    }

    let primary = if trimmed.contains("://") {
        trimmed.to_string()
    } else {
        format!("https://{}", trimmed.trim_end_matches('/'))
    };

    let parsed = url::Url::parse(&primary).map_err(|e| format!("invalid url: {}", e))?;
    let mut candidates = vec![parsed.to_string()];

    if parsed.scheme() == "https" {
        let mut http = parsed.clone();
        http.set_scheme("http").ok();
        candidates.push(http.to_string());
    }

    Ok(candidates)
}

async fn fetch_with_fallback(
    client: &Client,
    value: &str,
    entity_type: &str,
) -> Result<(String, reqwest::Response), Box<dyn std::error::Error + Send + Sync>> {
    let candidates = url_candidates(value, entity_type)?;
    let mut last_err: Option<reqwest::Error> = None;

    for candidate in candidates {
        match client.get(candidate.clone()).send().await {
            Ok(response) => return Ok((candidate, response)),
            Err(err) if err.is_connect() || err.is_timeout() => {
                last_err = Some(err);
            }
            Err(err) => return Err(err.into()),
        }
    }

    Err(last_err
        .map(|e| e.into())
        .unwrap_or_else(|| "no url candidates".into()))
}

fn extract_title(body: &str) -> Option<String> {
    let re = Regex::new(r"(?is)<title[^>]*>(.*?)</title>").ok()?;
    re.captures(body)
        .and_then(|c| c.get(1))
        .map(|m| {
            m.as_str()
                .replace('\n', " ")
                .split_whitespace()
                .collect::<Vec<_>>()
                .join(" ")
        })
        .filter(|s| !s.is_empty())
}

fn extract_analytics_ids(body: &str) -> Vec<String> {
    let patterns = [
        Regex::new(r"UA-\d{4,10}-\d{1,4}").unwrap(),
        Regex::new(r"GTM-[A-Z0-9]{6,12}").unwrap(),
        Regex::new(r"G-[A-Z0-9]{6,12}").unwrap(),
    ];

    let mut found = Vec::new();
    for pattern in patterns {
        for cap in pattern.find_iter(body) {
            let value = cap.as_str().to_string();
            if !found.contains(&value) {
                found.push(value);
            }
        }
    }
    found
}

fn extract_linked_hosts(body: &str, apex: &str) -> Vec<String> {
    let mut found = HashSet::new();
    let apex = normalize_host(apex);

    if let Ok(url_re) = Regex::new(r#"(?i)(?:href|src)=["']([^"']+)["']"#) {
        for cap in url_re.captures_iter(body) {
            if let Some(link) = cap.get(1) {
                if let Some(host) = host_from_link(link.as_str(), &apex) {
                    found.insert(host);
                }
            }
        }
    }

    let escaped = regex::escape(&apex);
    if let Ok(host_re) = Regex::new(&format!(r"(?i)(?:https?://)?([a-z0-9][\w-]*\.{escaped})")) {
        for cap in host_re.captures_iter(body) {
            if let Some(host) = cap.get(1) {
                if let Some(host) = normalize_discovered_host(host.as_str(), &apex) {
                    found.insert(host);
                }
            }
        }
    }

    found.into_iter().collect()
}

fn host_from_link(link: &str, apex: &str) -> Option<String> {
    if link.starts_with('/') || link.starts_with('#') || link.starts_with("mailto:") {
        return None;
    }
    let parsed = if link.contains("://") {
        url::Url::parse(link).ok()?
    } else {
        url::Url::parse(&format!("https://{link}")).ok()?
    };
    let host = parsed.host_str()?;
    normalize_discovered_host(host, apex)
}

fn normalize_discovered_host(host: &str, apex: &str) -> Option<String> {
    let host = normalize_host(host);
    if !is_crawlable_host(&host) {
        return None;
    }
    if is_subdomain_of(&host, apex) {
        return Some(host);
    }
    None
}

async fn fetch_favicon_hash(client: &Client, base_url: &str) -> Option<String> {
    let parsed = url::Url::parse(base_url).ok()?;
    let favicon_url = parsed.join("/favicon.ico").ok()?;
    let bytes = client.get(favicon_url).send().await.ok()?.bytes().await.ok()?;
    if bytes.is_empty() {
        return None;
    }
    let digest = Sha256::digest(&bytes);
    Some(format!("sha256:{:x}", digest))
}
