# Atlas Metrics

Operational metrics for monitoring ingestion progress, job queues, and graph growth.

## Endpoints

| Method | Path | Format |
|--------|------|--------|
| `GET` | `/metrics` | JSON (default) |
| `GET` | `/metrics?format=prometheus` | Prometheus text |
| `GET` | `/metrics/prometheus` | Prometheus text |

```bash
# JSON snapshot
curl http://localhost:8090/metrics

# Prometheus scrape target
curl http://localhost:8090/metrics/prometheus
```

## JSON response

```json
{
  "collected_at": "2026-06-24T12:00:00Z",
  "campaigns": {
    "running": 1,
    "completed": 5,
    "total": 6
  },
  "jobs": {
    "by_status": {
      "queued": 2,
      "running": 1,
      "completed": 40,
      "failed": 1
    },
    "by_collector": {
      "dns": { "queued": 0, "running": 0, "completed": 10, "failed": 0, "total": 10 }
    },
    "total": 44
  },
  "intelligence": {
    "domains": 1200,
    "hosts": 8500,
    "certificates": 50000,
    "certificate_names": 120000,
    "graph_edges": 95000,
    "dns_records": 3200,
    "rdap_records": 800,
    "http_fingerprints": 400,
    "entities": 200,
    "campaign_edges": 1500,
    "observations": 600
  },
  "ct": {
    "logs_total": 25,
    "logs_active": 5,
    "logs_readonly": 20,
    "entries_fetched": 1500000,
    "entries_available": 12000000,
    "aggregate_progress_pct": 12.5,
    "certificates": 50000,
    "certificate_names": 120000,
    "domains": 1200
  },
  "infra": {
    "nats": "connected",
    "redis": "connected"
  }
}
```

## Prometheus metrics

| Metric | Labels | Description |
|--------|--------|-------------|
| `atlas_campaigns` | `status` | Campaign count by status |
| `atlas_crawl_jobs` | `status` | Crawl job count by status |
| `atlas_crawl_jobs_by_collector` | `collector`, `status` | Jobs per collector |
| `atlas_intelligence_records` | `table` | Row counts for graph tables |
| `atlas_ct_logs` | `state` | CT logs by state |
| `atlas_ct_entries_fetched` | — | Sum of `last_fetched_index` |
| `atlas_ct_entries_available` | — | Sum of `last_tree_size` |
| `atlas_ct_aggregate_progress_pct` | — | Fetched ÷ available × 100 |
| `atlas_infra_up` | `component` | `1` if NATS/Redis connected |

### Prometheus scrape config

```yaml
scrape_configs:
  - job_name: atlas
    static_configs:
      - targets: ["control-api:8090"]
    metrics_path: /metrics/prometheus
    scrape_interval: 30s
```

## Use cases

| Question | Where to look |
|----------|---------------|
| Is CT backfill progressing? | `ct.aggregate_progress_pct` or `atlas_ct_aggregate_progress_pct` |
| Are workers keeping up? | `jobs.by_status.running` vs `queued` |
| Which collector fails most? | `jobs.by_collector.*.failed` |
| Graph growth rate | `intelligence.graph_edges`, `intelligence.domains` over time |
| Stack health | `infra.nats`, `infra.redis` |

## Related docs

| Guide | Description |
|-------|-------------|
| [Performance](./performance.md) | Benchmarks and campaign throughput |
| [Operations](./operations.md) | Deployment and health checks |
| [CT ingestor](./ct-ingestor.md) | Backfill configuration |
| [API guide](./api.md) | All endpoints |
