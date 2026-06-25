use serde::{Deserialize, Serialize};

#[derive(Debug, Deserialize, Clone)]
pub struct CrawlJob {
    pub job_id: String,
    pub campaign_id: String,
    pub entity_id: String,
    pub entity_type: String,
    pub entity_value: String,
    pub collector: String,
    pub depth: i32,
}

#[derive(Debug, Serialize, Clone)]
pub struct DiscoveredEntity {
    pub entity_type: String,
    pub value: String,
    pub relation: String,
}

#[derive(Debug, Serialize)]
pub struct CollectorResult {
    pub raw: serde_json::Value,
    pub discoveries: Vec<DiscoveredEntity>,
    pub source: String,
}

#[derive(Debug, Clone)]
pub struct CampaignLimits {
    pub max_depth: i32,
    pub max_entities: i32,
}

#[derive(Debug, Deserialize)]
pub struct EnrichDomainJob {
    pub domain: String,
    #[serde(default)]
    pub collectors: Vec<String>,
}
