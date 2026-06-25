use crate::collectors::ct_local;
use crate::models::{CollectorResult, CrawlJob};
use deadpool_postgres::Pool;

pub async fn collect(
    pool: &Pool,
    job: &CrawlJob,
) -> Result<CollectorResult, Box<dyn std::error::Error + Send + Sync>> {
    ct_local::collect_from_store(pool, job).await
}
