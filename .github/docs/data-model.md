# Atlas Data Model

Postgres schema for the intelligence graph and campaign orchestration layers.

## Intelligence graph (global)

These tables are **cross-campaign** — the long-lived source of truth for pivots and domain queries.

### `ct_logs`

Tracks public Certificate Transparency log sources and ingestion cursors.

| Column | Type | Description |
|--------|------|-------------|
| `id` | TEXT PK | Internal ID |
| `url` | TEXT UNIQUE | CT log base URL |
| `description` | TEXT | Log name from Google log list |
| `state` | TEXT | `active`, `readonly`, `inactive` |
| `last_tree_size` | BIGINT | Latest observed tree size |
| `last_fetched_index` | BIGINT | Next entry index to fetch |
| `updated_at` | TIMESTAMPTZ | Last sync time |

### `certificates`

Deduplicated certificate metadata from CT ingestion.

| Column | Type | Description |
|--------|------|-------------|
| `fingerprint_sha256` | TEXT UNIQUE | SHA-256 of DER |
| `subject_cn` | TEXT | Subject common name |
| `issuer` | TEXT | Issuer CN |
| `not_before` / `not_after` | TIMESTAMPTZ | Validity window |
| `raw_der` | BYTEA | Optional raw certificate |
| `first_seen` | TIMESTAMPTZ | First observation |
| `source_log_id` | TEXT FK | Originating CT log |

### `certificate_names`

SAN/CN names extracted from certificates.

| Column | Type | Description |
|--------|------|-------------|
| `certificate_id` | TEXT FK | Parent certificate |
| `name` | TEXT | DNS name (lowercase) |
| `registered_domain` | TEXT | Registrable apex |
| `is_wildcard` | BOOL | Wildcard cert flag |
| `first_seen` | TIMESTAMPTZ | First observation |

Unique on `(certificate_id, name)`.

### `domains`

Registrable domains discovered or seeded.

| Column | Type | Description |
|--------|------|-------------|
| `domain` | TEXT UNIQUE | Apex domain |
| `first_seen` / `last_seen` | TIMESTAMPTZ | Observation window |

### `rdap_records`

Cached RDAP responses per fetch.

| Column | Type | Description |
|--------|------|-------------|
| `domain_id` | TEXT FK | Parent domain |
| `registrar` / `registry` | TEXT | Normalised registration info |
| `created_at` / `updated_at` / `expires_at` | TIMESTAMPTZ | Registration dates |
| `statuses` | JSONB | RDAP status array |
| `nameservers` | JSONB | NS hostnames |
| `entities` | JSONB | Visible entity handles/orgs |
| `redacted` | BOOL | Registrant data redacted |
| `raw_json` | JSONB | Full RDAP JSON |
| `fetched_at` | TIMESTAMPTZ | Fetch timestamp |

### `dns_records`

Resolved DNS observations.

| Column | Type | Description |
|--------|------|-------------|
| `name` | TEXT | Query name |
| `record_type` | TEXT | `A`, `AAAA`, `NS`, `MX`, `TXT`, `CNAME` |
| `value` | TEXT | Record value |
| `ttl` | INT | Optional TTL |
| `first_seen` / `last_seen` | TIMESTAMPTZ | Observation window |

Unique on `(name, record_type, value)`.

### `hosts`

Discovered hostnames (apex or subdomain).

| Column | Type | Description |
|--------|------|-------------|
| `hostname` | TEXT UNIQUE | FQDN |
| `registered_domain` | TEXT | Parent apex |

### `http_fingerprints`

HTTP surface observations per host.

| Column | Type | Description |
|--------|------|-------------|
| `host_id` | TEXT FK | Parent host |
| `scheme` | TEXT | `http` or `https` |
| `status_code` | INT | HTTP status |
| `title` | TEXT | Page title |
| `server_header` | TEXT | `Server` header |
| `headers` | JSONB | Response headers |
| `favicon_hash` | TEXT | SHA-256 of favicon |
| `technologies` | JSONB | Reserved for future tech detection |
| `tracker_ids` | JSONB | Analytics IDs (UA-, GTM-, G-) |
| `final_url` | TEXT | URL after redirects |
| `fetched_at` | TIMESTAMPTZ | Fetch time |

### `graph_edges`

Typed relationships for pivot queries.

| Column | Type | Description |
|--------|------|-------------|
| `source_type` / `source_id` | TEXT | e.g. `domain` + `dom_...` |
| `relationship` | TEXT | e.g. `uses_ns`, `has_certificate` |
| `target_type` / `target_id` | TEXT | e.g. `nameserver` + `ns1.example.net` |
| `confidence` | REAL | Default `1.0` |
| `source` | TEXT | Attribution (`rdap`, `dns`, `ct`, `campaign:dns`, …) |
| `first_seen` / `last_seen` | TIMESTAMPTZ | Edge lifetime |

Unique on `(source_type, source_id, relationship, target_type, target_id)`.

### Common relationships

| Relationship | Source → Target | Example |
|--------------|-----------------|---------|
| `has_subdomain` | domain → host | `example.com` → `api.example.com` |
| `has_certificate` | domain → certificate | fingerprint SHA-256 |
| `registered_with` | domain → registrar | RDAP registrar name |
| `uses_ns` | domain → nameserver | `ns1.cloudflare.com` |
| `uses_mx` | domain → mx | `mail.example.net` |
| `resolves_to` | domain → ip | `203.0.113.1` |
| `has_entity` | domain → entity | RDAP handle (non-redacted) |
| `shares_favicon` | domain → favicon_hash | SHA-256 |
| `shares_tracker` | domain → tracker_id | `UA-12345` |
| `cname_to` | domain → host | CNAME target |
| `redirects_to` | domain → url | HTTP redirect |

### `ingestor_config`

Runtime configuration for `ct-ingestor` (key `ct`).

```json
{
  "target_tlds": ["com", "net", "org", "io", "co.uk"],
  "backfill_mode": true,
  "include_readonly": true,
  "batches_per_cycle": 20,
  "batch_size": 512
}
```

## Campaign layer (per run)

| Table | Purpose |
|-------|---------|
| `campaigns` | Discovery run config and status |
| `entities` | Canonical entities `(type, value)` |
| `campaign_entities` | Campaign membership + depth |
| `observations` | Raw collector JSON per entity |
| `edges` | Campaign-scoped relationships with evidence |
| `crawl_jobs` | Per-entity, per-collector job state |
| `campaign_events` | Audit/event log |
| `expansion_suggestions` | Pending/approved expansion candidates |

Campaign discoveries are **synced** into the intelligence graph (`domains`, `graph_edges`, etc.) when collectors complete.

## Entity types (campaign)

| Type | Examples |
|------|----------|
| `domain` | `example.com` |
| `subdomain` | `api.example.com` |
| `ip` | `203.0.113.1` |
| `nameserver` | `ns1.example.net` |
| `mx` | `mail.example.net` |
| `certificate` | SHA-256 fingerprint |
| `organisation` | TLS cert org |
| `registrar` | RDAP registrar |
| `analytics_id` | `UA-12345` |
| `favicon_hash` | SHA-256 |
| `url` | `https://example.com/` |
| `email` | seed classification only |

## Related docs

| Guide | Description |
|-------|-------------|
| [Architecture](./architecture.md) | How data flows into tables |
| [Pivots](./pivots.md) | Querying `graph_edges` |
| [CT ingestor](./ct-ingestor.md) | Populating certificates |
