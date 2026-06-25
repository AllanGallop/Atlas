# Atlas Control API

- **OpenAPI spec:** [openapi.yaml](./openapi.yaml)
- **Base URL (local):** `http://localhost:8090`

## Quick start

```bash
# Health check
curl http://localhost:8090/health

# Seed a domain for intelligence enrichment
curl -X POST http://localhost:8090/domains \
  -H "Content-Type: application/json" \
  -d '{"domains": ["example.com"], "collectors": ["ct", "rdap", "dns"]}'

# Query intelligence
curl http://localhost:8090/domains/example.com
curl http://localhost:8090/domains/example.com/subdomains
curl http://localhost:8090/domains/example.com/pivots

# Start a discovery campaign
curl -X POST http://localhost:8090/campaigns \
  -H "Content-Type: application/json" \
  -d '{
    "seeds": ["example.com"],
    "collectors": ["dns", "http", "tls", "ct", "rdap"],
    "limits": { "max_depth": 2, "max_entities": 5000 }
  }'
```

## Endpoints

### Health

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Liveness check |
| `GET` | `/metrics` | Operational metrics (JSON) |
| `GET` | `/metrics/prometheus` | Prometheus exposition format |

### Domain intelligence

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/domains` | Seed domains and queue enrichment |
| `GET` | `/domains/{domain}` | Domain record + graph edges |
| `GET` | `/domains/{domain}/subdomains` | Subdomains from CT store |
| `GET` | `/domains/{domain}/certificates` | Certificates covering the domain |
| `GET` | `/domains/{domain}/rdap` | Latest RDAP record |
| `GET` | `/domains/{domain}/dns` | Cached DNS records |
| `GET` | `/domains/{domain}/pivots` | Pivot summary for the domain |
| `POST` | `/domains/{domain}/enrich` | Re-queue enrichment for one domain |

### Pivots

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/pivots/nameserver/{ns}` | Domains using a nameserver |
| `GET` | `/pivots/registrar/{registrar}` | Domains with matching registrar |
| `GET` | `/pivots/certificate/{fingerprint}` | Domains on a certificate |
| `GET` | `/pivots/mx/{mx}` | Domains using an MX host |
| `GET` | `/pivots/favicon/{hash}` | Domains sharing a favicon hash |
| `GET` | `/pivots/entity/{handle}` | Domains linked to an RDAP entity handle |
| `GET` | `/pivots/tracker/{id}` | Domains sharing an analytics/tracker ID |

### CT ingestor

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/ct/config` | Current ingestor configuration |
| `PUT` | `/ct/config` | Update ingestor configuration |
| `POST` | `/ct/backfill` | Enable TLD-targeted backfill |
| `GET` | `/ct/status` | Log progress and store counts |

### Campaigns

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/campaigns` | List campaigns (`?status=running`) |
| `POST` | `/campaigns` | Start a discovery campaign |
| `GET` | `/campaigns/{id}` | Campaign status + expansion suggestions |
| `GET` | `/campaigns/{id}/progress` | Job and entity counts |
| `GET` | `/campaigns/{id}/events` | NDJSON event stream |
| `GET` | `/campaigns/{id}/entities` | Discovered entities |
| `GET` | `/campaigns/{id}/edges` | Relationship edges |
| `GET` | `/campaigns/{id}/report` | Compiled campaign report |
| `POST` | `/campaigns/{id}/expand` | Approve expansion entities |

## Seed domains

`POST /domains`

```json
{
  "domains": ["example.com", "example.org"],
  "collectors": ["ct", "rdap", "dns", "http", "tls"]
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `domains` | yes | Registrable domain names |
| `collectors` | no | Defaults to `dns`, `http`, `tls`, `ct`; `rdap` is added if omitted |

**Response** `202 Accepted`

```json
{
  "domains": [
    { "domain": "example.com", "domain_id": "dom_...", "status": "queued" }
  ],
  "jobs_queued": 1,
  "collectors": ["dns", "http", "tls", "ct", "rdap"]
}
```

## Create campaign

`POST /campaigns`

```json
{
  "seeds": ["example.com", "203.0.113.10"],
  "collectors": ["dns", "http", "tls", "ct", "rdap"],
  "depth": 2,
  "limits": {
    "max_depth": 2,
    "max_entities": 5000
  }
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `seeds` | yes | Domains, subdomains, IPs, URLs, or emails |
| `collectors` | no | Default: `dns`, `http`, `tls`, `ct` |
| `depth` | no | Legacy alias for `limits.max_depth` |
| `limits.max_depth` | no | Default `2` |
| `limits.max_entities` | no | Default `5000` |

Discoveries beyond seeds require explicit approval via `POST /campaigns/{id}/expand`.

## Campaign status filter

`GET /campaigns?status=running`

Accepts canonical statuses and aliases:

| Alias | Expands to |
|-------|------------|
| `active` | `queued`, `running`, `expanding`, `waiting_for_rate_limit` |
| `done` | `completed`, `completed_with_errors`, `cancelled` |
| `failed` | `completed_with_errors` |

## CT backfill

`POST /ct/backfill`

```json
{
  "target_tlds": ["com", "io", "co.uk", "net", "org"],
  "include_readonly": true,
  "batches_per_cycle": 30,
  "batch_size": 512
}
```

Sets `backfill_mode: true` and updates `ingestor_config` in Postgres. The `ct-ingestor` service reads this on each poll cycle.

## Related docs

| Guide | Description |
|-------|-------------|
| [Architecture](./architecture.md) | Components and flows |
| [Data model](./data-model.md) | Tables and relationships |
| [Pivots](./pivots.md) | Pivot semantics and limitations |
| [Operations](./operations.md) | Environment variables |
