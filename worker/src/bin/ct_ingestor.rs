//! Standalone CT log ingestor — runs continuously, independent of campaign workers.

#[path = "../ct_ingestor/mod.rs"]
mod ct_ingestor;

use ct_ingestor::create_pool;
use reqwest::Client;
use std::time::Duration;

#[tokio::main]
async fn main() {
    let database_url = std::env::var("DATABASE_URL")
        .unwrap_or_else(|_| "postgres://atlas:atlas@postgres:5432/atlas?sslmode=disable".into());

    let pool = create_pool(&database_url);
    let client = Client::builder()
        .timeout(Duration::from_secs(60))
        .user_agent("AtlasCTIngestor/0.1")
        .build()
        .expect("http client");

    println!("atlas-ct-ingestor starting");

    if let Err(e) = ct_ingestor::run_ingestor(pool, client).await {
        eprintln!("atlas-ct-ingestor fatal: {}", e);
        std::process::exit(1);
    }
}
