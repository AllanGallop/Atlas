# Operations

Deployment, configuration, and tuning for the Atlas docker-compose stack.

## Quick start

```bash
docker compose up --build
```

| Service | Host port | Description |
|---------|-----------|-------------|
| control-api | 8090 | REST API |
| Postgres | 5433 | Database |
| Redis | 6380 | Job dedupe |
| NATS | 4223 | Job queue |
| NATS monitoring | 8223 | HTTP monitor |

## Services

```yaml
services:
  postgres      # postgres:16-alpine
  redis         # redis:7-alpine
  nats          # nats:2.10-alpine (-js)
  control-api   # Go API
  worker        # Rust collectors
  ct-ingestor   # Rust CT log ingestion
```

## Environment variables

### control-api

| Variable | Default | Description |
|----------|---------|-------------|
| `LISTEN_ADDR` | `:8090` | HTTP listen address |
| `DATABASE_URL` | `postgres://atlas:atlas@postgres:5432/atlas` | Postgres DSN |
| `NATS_URL` | `nats://nats:4222` | NATS connection |
| `REDIS_URL` | `redis://redis:6379/0` | Redis connection |

### atlas-worker

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | (same as above) | Postgres DSN |
| `NATS_URL` | `nats://nats:4222` | NATS connection |
| `WORKER_CONCURRENCY` | `20` | Max concurrent jobs (1–100) |

### ct-ingestor

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | (same as above) | Postgres DSN |
| `CT_POLL_INTERVAL_SECS` | `30` | Seconds between ingest cycles |

Runtime TLD/backfill settings are managed via `/ct/config` and stored in `ingestor_config`.

## Local development

```bash
# Terminal 1 — API (requires Postgres, Redis, NATS)
cd control && go run .

# Terminal 2 — worker
cd worker && cargo run --bin atlas-worker

# Terminal 3 — CT ingestor
cd worker && cargo run --bin atlas-ct-ingestor
```

Point `DATABASE_URL`, `NATS_URL`, and `REDIS_URL` at localhost ports from docker-compose if infra runs in Docker.

## Postgres

Default connection:

```
postgres://atlas:atlas@localhost:5433/atlas?sslmode=disable
```

Schema migrations run inline on control-api startup (`CREATE TABLE IF NOT EXISTS`).

Tuning (docker-compose):

```yaml
command: ["postgres", "-c", "max_connections=300", "-c", "shared_buffers=256MB"]
```

## Recommended startup order

1. `postgres`, `redis`, `nats` healthy
2. `control-api` (runs migrations)
3. `ct-ingestor` (begin CT backfill)
4. `worker`
5. `POST /ct/backfill` with target TLDs
6. `POST /domains` or `POST /campaigns`

## Health checks

```bash
curl http://localhost:8090/health
curl http://localhost:8090/metrics
curl http://localhost:8090/metrics/prometheus
curl http://localhost:8090/ct/status
```

## Testing

| Layer | Command |
|-------|---------|
| Go unit | `cd control && go test ./...` |
| Rust unit | `cd worker && cargo test --bin atlas-worker --bin atlas-ct-ingestor` |
| E2E | `docker compose up -d --build && bash tests/e2e.sh` |

The e2e script verifies health, CT config/status, domain seeding, campaign creation, progress polling, and report generation against `http://localhost:8090`.

## Tuning

| Goal | Action |
|------|--------|
| Faster CT coverage | Increase `batches_per_cycle` and `batch_size` via `/ct/backfill` |
| Lower CT log pressure | Decrease `batch_size`, increase `CT_POLL_INTERVAL_SECS` |
| More parallel enrichment | Raise `WORKER_CONCURRENCY` |
| Smaller Postgres | Narrow `target_tlds` to domains you care about |

## Data retention

No automatic expiry is configured in MVP. `rdap_records` are append-only per fetch; collectors use the latest row. Plan retention policies before production deployment.

## Security notes

- Atlas queries **public** data sources and targets you seed.
- Only enumerate domains and infrastructure you are authorised to investigate.
- Do not expose the API to the public internet without authentication.
- RDAP and CT sources impose rate limits — aggressive backfill may trigger throttling.

## Related docs

| Guide | Description |
|-------|-------------|
| [Architecture](./architecture.md) | Component overview |
| [CT ingestor](./ct-ingestor.md) | Backfill configuration |
| [API guide](./api.md) | Endpoint reference |
