# Atlas Performance Guide

## Test setup

| Component | Configuration |
|-----------|---------------|
| Stack | `docker compose up -d --build` (default `docker-compose.yml`) |
| Benchmark script | `tests/bench.sh` |
| Sweep script | `tests/bench-sweep.sh` (100 → 5,000 jobs) |
| Default collector | `ct` (local Postgres lookup — fastest path) |
| Seeds | `bench-{timestamp}-{n}.invalid` (no graph expansion) |
| Campaign limits | `max_depth: 1`, `max_entities: jobs + 10` |
| Worker env | `WORKER_CONCURRENCY=20` |
| Postgres | `max_connections=300`, `shared_buffers=256MB` |
| Poll endpoint | `GET /campaigns/{id}/progress` |

Jobs are published to NATS **during** `POST /campaigns` (one seed at a time). Workers therefore run **concurrently** with campaign creation. End-to-end throughput is limited by the queue path unless a collector is network-bound (RDAP, HTTP, TLS).

## Running benchmarks

```bash
# Start the stack
docker compose up -d --build

# Default: 500 jobs, CT collector
bash tests/bench.sh

# Custom run count, collector, and timeout
RUNS=1000 COLLECTORS=ct TIMEOUT_SEC=600 bash tests/bench.sh

# Full sweep table (100, 500, 1,000, 5,000)
bash tests/bench-sweep.sh

# From inside the compose network
API=http://control-api:8090 RUNS=1000 bash tests/bench.sh
```

On Windows without a local shell, run via Docker:

```bash
docker run --rm --network atlas_default \
  -v "$(pwd)/tests:/tests" alpine \
  sh -c "apk add --no-cache curl bash python3 >/dev/null && \
    API=http://control-api:8090 COLLECTORS=ct bash /tests/bench-sweep.sh"
```

### Metrics reported

| Metric | Measures |
|--------|----------|
| **Queue time** | `POST /campaigns` until HTTP response (sequential seed upsert + job insert + NATS publish) |
| **Worker time** | Time from queue response until `completed_jobs + failed_jobs == total_jobs` |
| **Total time** | Wall-clock end-to-end |

Because workers consume jobs while the API is still queuing, **worker time** often reflects only tail drain (remaining backlog after the POST returns). Use **total rate** for end-to-end throughput.

## Reference results

Measured on Windows 11, Docker Desktop, single `worker` replica, `COLLECTORS=ct`, `WORKER_CONCURRENCY=20`.

### Campaign throughput (CT collector)

| Jobs | Queue (ms) | Worker (ms) | Total (ms) | Queue rate | Worker rate | Total rate |
|------|------------|-------------|------------|------------|-------------|------------|
| 100 | 1,750 | 29 | 1,811 | 57/s | 3,448/s | 55/s |
| 500 | 8,507 | 24 | 8,562 | 59/s | 20,833/s | 58/s |
| 1,000 | 17,202 | 25 | 17,256 | 58/s | 40,000/s | 58/s |
| 5,000 | 88,356 | 34 | 88,428 | 57/s | 147,059/s | 57/s |

### Observations

**Queue path is the bottleneck** for CT and DNS at default settings (~57 jobs/sec). Each seed triggers entity upsert, optional domain row, crawl job insert, Redis dedupe, and NATS publish in a serial loop inside `createCampaign`.

**Workers keep up with CT jobs.** Tail drain after the POST returns is typically under 50 ms even at 5,000 jobs because lookups hit local Postgres, not external CT APIs.

**Collector choice changes worker cost, not queue cost.** Queue time is identical for the same job count; slow collectors (RDAP, HTTP, TLS) increase overlap pressure and total time when workers cannot drain as fast as jobs are published.

| Collector | Typical worker bound | Notes |
|-----------|---------------------|-------|
| `ct` | Local Postgres | Fastest; benchmark default |
| `dns` | Resolver RTT | Similar total rate at 100 jobs in reference run |
| `rdap` | Registry rate limits | 7-day cache helps repeat lookups |
| `http` / `tls` | Target response time | Network and timeout dependent |

## Tuning

### Worker throughput

| Variable | Default | Effect |
|----------|---------|--------|
| `WORKER_CONCURRENCY` | `20` | Max concurrent jobs per worker (1–100) |

Scale workers horizontally:

```bash
docker compose up -d --scale worker=4
```

Keep Postgres headroom: `max_connections >= (worker_replicas × expected_pool) + control-api`.

### Queue throughput

Campaign creation is currently sequential per seed. For large seed batches, prefer:

- `POST /domains` for intelligence seeding (separate code path)
- Multiple smaller campaigns in parallel
- Future bulk campaign API if batch sizes exceed a few thousand seeds

### Postgres

```yaml
postgres:
  command: ["postgres", "-c", "max_connections=300", "-c", "shared_buffers=256MB"]
```

Increase `max_connections` and `shared_buffers` when scaling workers or running heavy CT ingestion alongside campaigns.

## Operational metrics

Runtime counters (graph growth, job queues, CT progress) are exposed separately from benchmarks:

```bash
curl http://localhost:8090/metrics
curl http://localhost:8090/metrics/prometheus
```

See the [metrics guide](./metrics.md) for JSON fields and Prometheus scrape config.

### Monitoring during a benchmark

```bash
# Campaign progress
curl http://localhost:8090/campaigns/{id}/progress

# Global job backlog
curl http://localhost:8090/metrics | jq '.jobs.by_status'

# Worker logs
docker compose logs -f worker
```

## Related scripts

| Script | Purpose |
|--------|---------|
| `tests/bench.sh` | Queue + worker end-to-end benchmark |
| `tests/bench-sweep.sh` | Run bench.sh at 100 / 500 / 1,000 / 5,000 jobs |
| `tests/e2e.sh` | Functional correctness |

## Related docs

| Guide | Description |
|-------|-------------|
| [Metrics](./metrics.md) | `/metrics` and Prometheus |
| [Operations](./operations.md) | Env vars and deployment |
| [Collectors](./collectors.md) | Per-collector behaviour |
| [API guide](./api.md) | Campaign and domain endpoints |
