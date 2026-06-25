# Pivots

Atlas implements **reverse intelligence as graph pivots** over collected public data — not as a complete reverse WHOIS database.

Given one infrastructure or registration artifact, pivot endpoints return other domains that share it.

## Philosophy

| Traditional reverse WHOIS | Atlas pivots |
|---------------------------|--------------|
| Promises registrant identity | Surfaces **observable** relationships |
| Often stale or redacted | Source-attributed with timestamps |
| Single data source | CT + RDAP + DNS + HTTP combined |

Registrant data is frequently redacted in RDAP. Atlas exposes pivots where data **is** visible: nameservers, certificates, MX, favicon hashes, tracker IDs, and non-redacted entity handles.

## Pivot endpoints

| Endpoint | Finds domains that… |
|----------|---------------------|
| `GET /pivots/nameserver/{ns}` | Use the same NS record |
| `GET /pivots/registrar/{registrar}` | Share a registrar (RDAP) |
| `GET /pivots/certificate/{fingerprint}` | Appear on the same TLS cert |
| `GET /pivots/mx/{mx}` | Use the same MX host |
| `GET /pivots/favicon/{hash}` | Share a favicon SHA-256 |
| `GET /pivots/entity/{handle}` | Link to the same RDAP entity handle |
| `GET /pivots/tracker/{id}` | Share an analytics/tracker ID |

### Example workflow

```bash
# Start from a seed
curl -X POST http://localhost:8090/domains \
  -d '{"domains":["example.com"]}'

# See what pivots exist
curl http://localhost:8090/domains/example.com/pivots

# Pivot on a shared nameserver
curl http://localhost:8090/pivots/nameserver/ns1.cloudflare.com

# Pivot on a certificate fingerprint
curl http://localhost:8090/pivots/certificate/a1b2c3...
```

### Response shape

```json
{
  "pivot_type": "nameserver",
  "value": "ns1.cloudflare.com",
  "domains": [
    {
      "domain": "other-example.com",
      "first_seen": "2026-06-24T12:00:00Z",
      "last_seen": "2026-06-24T12:00:00Z"
    }
  ],
  "count": 1
}
```

Results are capped at **500 domains** per pivot query.

## Domain pivot summary

`GET /domains/{domain}/pivots` returns artifacts you can pivot on:

```json
{
  "domain": "example.com",
  "pivots": {
    "nameservers": ["ns1.example.net"],
    "registrars": ["Example Registrar, Inc."],
    "certificates": ["a1b2c3..."],
    "mx_hosts": ["mail.example.net"],
    "favicon_hashes": ["d4e5f6..."],
    "entity_handles": ["EXAMPLE-REG"]
  }
}
```

## How edges are created

| Source | Relationships written |
|--------|----------------------|
| RDAP | `registered_with`, `uses_ns`, `has_entity` |
| DNS | `resolves_to`, `uses_ns`, `uses_mx`, `cname_to` |
| CT | `has_subdomain`, `has_certificate` |
| HTTP | `shares_favicon`, `shares_tracker` |
| TLS | `has_certificate`, `has_subdomain`, `registered_with` |
| Campaign sync | Same mappings, source `campaign:{collector}` |

## Confidence and provenance

Each `graph_edges` row includes:

- `source` — which collector wrote the edge (`rdap`, `dns`, `campaign:http`, …)
- `first_seen` / `last_seen` — when the relationship was first and last observed
- `confidence` — currently `1.0` for direct observations

## Limitations

- **Incomplete coverage:** Only domains in the CT store or explicitly enriched appear.
- **Redacted RDAP:** Entity/registrant pivots require non-redacted handles.
- **Shared infrastructure false positives:** CDN nameservers, shared hosting, and SaaS favicons may link unrelated domains.
- **No guarantee of ownership:** Shared artifacts suggest association, not confirmed ownership.

Always corroborate pivot results with additional evidence.

## Related docs

| Guide | Description |
|-------|-------------|
| [Data model](./data-model.md) | `graph_edges` schema |
| [API guide](./api.md) | Endpoint reference |
| [Collectors](./collectors.md) | How edges are populated |
