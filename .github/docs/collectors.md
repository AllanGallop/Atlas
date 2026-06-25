# Collectors

Atlas workers run **collectors** asynchronously via NATS. Each collector accepts a seed entity, performs external lookups, stores an observation, and returns discoveries for graph expansion.

## Overview

| Collector | NATS subject | Seed types | Intelligence tables |
|-----------|--------------|------------|---------------------|
| `dns` | `atlas.jobs.dns` | domain, subdomain, nameserver | `dns_records`, `graph_edges` |
| `http` | `atlas.jobs.http` | domain, subdomain, url, ip | `http_fingerprints`, `graph_edges` |
| `tls` | `atlas.jobs.tls` | domain, subdomain, url, ip | `graph_edges` |
| `ct` | `atlas.jobs.ct` | domain, subdomain | reads local CT store |
| `rdap` | `atlas.jobs.rdap` | domain, subdomain (→ apex) | `rdap_records`, `graph_edges` |

Direct enrichment (`POST /domains`) uses the same collector logic via `atlas.enrich.domain`.

## DNS

Resolves:

- **A** / **AAAA** — IPv4/IPv6 addresses
- **NS** — nameservers
- **MX** — mail exchangers
- **TXT** — text records
- **CNAME** — canonical name aliases

Discoveries: `ip`, `nameserver`, `mx`, `txt`, `domain`/`subdomain` (via CNAME).

Campaign edges: `RESOLVES_TO`, `USES_NS`, `USES_MX`, `HAS_TXT`, `CNAME_TO`.

## HTTP

Fetches `http://` and `https://` (with redirect following).

Extracts:

- Status code, final URL, redirect chain
- Page `<title>`
- Response headers (`Server`, etc.)
- Favicon SHA-256 hash
- Analytics IDs (`UA-`, `GTM-`, `G-` patterns)
- Linked subdomains from page body (same apex)

Campaign edges: `REDIRECTS_TO`, `SHARES_FAVICON`, `SHARES_ANALYTICS_ID`, `LINKED_FROM`.

## TLS

Connects to port 443 and inspects the leaf certificate.

Extracts:

- SHA-256 fingerprint
- Subject, issuer, organisation
- Validity dates
- Subject Alternative Names (SANs)

Campaign edges: `HAS_CERT`, `CERT_HAS_SAN`, `REGISTERED_WITH`.

## CT (local store)

**Does not call crt.sh.** Queries the Postgres CT ingestion store:

- `certificate_names` for subdomains under the seed apex
- `certificates` for metadata (issuer, validity, fingerprint)

Requires `ct-ingestor` to have ingested relevant certificates first. Use [CT backfill](./ct-ingestor.md) to populate TLDs of interest.

Campaign edges: `FOUND_IN_CT`.

## RDAP

Resolves RDAP server via [IANA bootstrap](https://data.iana.org/rdap/dns.json), fetches domain JSON, normalises:

- Registrar, registry
- Registration / update / expiry dates
- Nameservers, statuses
- Visible entity handles (with redaction flags)

**Caching:** 7-day TTL in `rdap_records`. Respects 429 rate limits with backoff.

Campaign edges: `USES_NS`, `REGISTERED_WITH`.

## Collector routing

Not every entity type runs every collector:

| Entity | Collectors |
|--------|------------|
| `domain`, `subdomain` | dns, http, tls, ct, rdap |
| `ip` | http, tls |
| `nameserver` | dns |
| `url` | dns, http, tls |

## Campaign expansion

Discoveries at depth `N+1` become **expansion suggestions** until approved:

```bash
curl -X POST http://localhost:8090/campaigns/{id}/expand \
  -H "Content-Type: application/json" \
  -d '{ "entity_ids": ["ent_..."] }'
```

Respects `max_depth` and `max_entities` campaign limits.

## Global graph sync

When a campaign collector completes, discoveries are mirrored into:

- `domains`, `hosts`
- `graph_edges` (source: `campaign:{collector}`)

This lets pivot APIs surface infrastructure found during campaigns without re-querying campaign tables.

## Related docs

| Guide | Description |
|-------|-------------|
| [CT ingestor](./ct-ingestor.md) | Feeding the CT collector |
| [API guide](./api.md) | Starting campaigns and enrichment |
| [Data model](./data-model.md) | Where observations land |
