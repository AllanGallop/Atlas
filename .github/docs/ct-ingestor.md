# CT Ingestor

`atlas-ct-ingestor` streams Certificate Transparency logs directly into Postgres. It is the upstream data source for the `ct` collector and subdomain pivot APIs.

Atlas **does not scrape crt.sh**.

## How it works

```mermaid
flowchart LR
    LIST[Google CT log list] --> SYNC[sync_log_list]
    SYNC --> DB[(ct_logs)]
    DB --> POLL[poll get-sth / get-entries]
    POLL --> DECODE[decode X.509 / precert]
    DECODE --> FILTER[TLD filter]
    FILTER --> STORE[certificates + certificate_names]
    STORE --> DOMAINS[domains upsert]
```

1. Fetch `https://www.gstatic.com/ct/log_list/v3/log_list.json`
2. Upsert logs into `ct_logs` with state (`active`, `readonly`, `inactive`)
3. For each log: read `get-sth` tree size, fetch `get-entries` in batches
4. Decode `leaf_input` → DER certificate
5. Extract SAN/CN, issuer, validity, SHA-256 fingerprint
6. Store in `certificates` and `certificate_names` (deduplicated)
7. Advance `last_fetched_index`

## Runtime configuration

Config is stored in Postgres (`ingestor_config`, key `ct`) and read every poll cycle.

| Field | Default | Description |
|-------|---------|-------------|
| `target_tlds` | `com`, `net`, `org`, `io`, `co.uk`, `com.au` | Only store certs with names in these TLDs |
| `backfill_mode` | `true` (seed) | Process multiple batches per log per cycle |
| `include_readonly` | `true` (seed) | Also ingest historical readonly logs |
| `batches_per_cycle` | `20` | Batches per log when backfill enabled |
| `batch_size` | `512` | Entries per `get-entries` request |

### View config

```bash
curl http://localhost:8090/ct/config
```

### Update config

```bash
curl -X PUT http://localhost:8090/ct/config \
  -H "Content-Type: application/json" \
  -d '{
    "target_tlds": ["com", "io"],
    "backfill_mode": true,
    "include_readonly": true,
    "batches_per_cycle": 25,
    "batch_size": 512
  }'
```

### Start backfill

`POST /ct/backfill` enables backfill mode and sets TLD targets in one call:

```bash
curl -X POST http://localhost:8090/ct/backfill \
  -H "Content-Type: application/json" \
  -d '{
    "target_tlds": ["com", "io", "co.uk", "net", "org"],
    "include_readonly": true,
    "batches_per_cycle": 30,
    "batch_size": 512
  }'
```

### Monitor progress

```bash
curl http://localhost:8090/ct/status
```

Returns per-log `last_fetched_index`, `last_tree_size`, `progress_pct`, plus global certificate/domain counts.

## TLD filtering

When `target_tlds` is non-empty, certificates are stored only if at least one SAN/CN maps to a registrable domain ending in a listed TLD.

Examples with `target_tlds: ["com", "co.uk"]`:

| Name | Stored? |
|------|---------|
| `api.example.com` | yes |
| `shop.example.co.uk` | yes |
| `node.example.io` | no |

Empty `target_tlds` disables filtering (store all decoded names).

## Rate limiting

- 200–500 ms delay between batches per log
- 15–30 s poll interval between full cycles (`CT_POLL_INTERVAL_SECS`)
- Respects CT log operator rate limits; backs off on HTTP errors

## Local development

```bash
cd worker && cargo run --bin atlas-ct-ingestor
```

Environment:

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | `postgres://atlas:atlas@postgres:5432/atlas` | Postgres connection |
| `CT_POLL_INTERVAL_SECS` | `30` | Seconds between ingest cycles |

## Docker

```yaml
ct-ingestor:
  build:
    context: ./worker
    dockerfile: Dockerfile.ct-ingestor
  environment:
    DATABASE_URL: postgres://atlas:atlas@postgres:5432/atlas?sslmode=disable
    CT_POLL_INTERVAL_SECS: "15"
```

## Operational notes

- **Cold start:** Subdomains appear only after relevant certs are ingested. Backfill `.com` first for broad coverage.
- **Readonly logs:** Historical logs are large; `progress_pct` shows per-log completion.
- **Dedup:** Certificates deduplicate on `fingerprint_sha256`; names on `(certificate_id, name)`.
- **Malformed entries:** Skipped silently; ingestion continues.

## Related docs

| Guide | Description |
|-------|-------------|
| [Collectors](./collectors.md) | CT collector (local lookup) |
| [Data model](./data-model.md) | `ct_logs`, `certificates` schema |
| [Operations](./operations.md) | Stack deployment |
