# Atlas Architecture

Component overview and operational flows for the default docker-compose stack.

## Overview

Atlas is a **domain intelligence graph** service. It ingests public Certificate Transparency logs, RDAP registration data, and DNS records; normalises them into Postgres; and exposes pivot-friendly APIs for external surface discovery.

```mermaid
flowchart TB
    subgraph clients [Clients]
        CLI[curl / scripts]
        APP[downstream services]
    end

    subgraph control [Control Plane]
        API[Go control-api]
    end

    subgraph messaging [Job Queue]
        NATS[NATS]
    end

    subgraph cache [Dedupe]
        REDIS[Redis]
    end

    subgraph storage [State]
        PG[(Postgres)]
    end

    subgraph ingest [Ingestion]
        CT[ct-ingestor]
    end

    subgraph workers [Rust Workers]
        W[atlas-worker]
    end

    subgraph sources [Public Data Sources]
        CTLOGS[CT logs]
        RDAP[RDAP servers]
        DNSP[DNS resolvers]
        WEB[HTTP/TLS targets]
    end

    CLI --> API
    APP --> API
    API --> PG
    API --> NATS
    API --> REDIS
    NATS --> W
    W --> PG
    W --> RDAP
    W --> DNSP
    W --> WEB
    CT --> CTLOGS
    CT --> PG
```

| Component | Role |
|-----------|------|
| **control-api** | REST API — campaigns, domain intelligence, pivots, CT config |
| **atlas-worker** | Async collectors (DNS, HTTP, TLS, CT local lookup, RDAP) |
| **ct-ingestor** | Continuous CT log ingestion; TLD-targeted backfill |
| **NATS** | Job queue (`atlas.jobs.*`, `atlas.enrich.domain`) |
| **Postgres** | Intelligence graph + campaign orchestration state |
| **Redis** | Hot dedupe for campaign job enqueue |

## Intelligence flow

```mermaid
sequenceDiagram
    participant C as Client
    participant API as control-api
    participant PG as Postgres
    participant N as NATS
    participant W as worker
    participant CT as ct-ingestor

    Note over CT,PG: Background — CT logs streamed into certificates / certificate_names
    CT->>PG: upsert certs, domains, names

    C->>API: POST /domains
    API->>PG: upsert domains
    API->>N: publish atlas.enrich.domain
    N->>W: enrich job
    W->>PG: CT lookup (local store)
    W->>PG: RDAP fetch + cache
    W->>PG: DNS resolve
    W->>PG: graph_edges
    C->>API: GET /domains/{domain}/pivots
    API->>PG: query graph
```

## Campaign flow

Campaigns add **controlled expansion** — discoveries become suggestions until explicitly approved.

```mermaid
sequenceDiagram
    participant C as Client
    participant API as control-api
    participant PG as Postgres
    participant N as NATS
    participant W as worker

    C->>API: POST /campaigns
    API->>PG: insert campaign, seeds, crawl_jobs
    API->>N: publish atlas.jobs.{collector}
    N->>W: crawl job
    W->>PG: observations, entities, edges
    W->>PG: sync to global graph (domains, graph_edges)
    C->>API: GET /campaigns/{id}/entities
    C->>API: POST /campaigns/{id}/expand
    API->>N: enqueue approved entities
```

## Campaign lifecycle

```mermaid
stateDiagram-v2
    [*] --> queued: POST /campaigns
    queued --> running: jobs published
    running --> expanding: POST /expand
    expanding --> running: expansion jobs queued
    running --> completed: all jobs done
    running --> completed_with_errors: any job failed
    completed --> [*]
    completed_with_errors --> [*]
```

## Data layers

| Layer | Tables | Scope |
|-------|--------|-------|
| **Intelligence graph** | `domains`, `certificates`, `rdap_records`, `dns_records`, `graph_edges`, … | Global, cross-campaign |
| **Campaign state** | `campaigns`, `entities`, `edges`, `crawl_jobs`, `observations` | Per discovery run |

Campaign collector output is **mirrored** into the intelligence graph so pivots work across both direct seeding and campaign discoveries.

## NATS subjects

| Subject | Publisher | Consumer |
|---------|-----------|----------|
| `atlas.jobs.dns` | control-api | worker |
| `atlas.jobs.http` | control-api | worker |
| `atlas.jobs.tls` | control-api | worker |
| `atlas.jobs.ct` | control-api | worker |
| `atlas.jobs.rdap` | control-api | worker |
| `atlas.enrich.domain` | control-api | worker |

## Related docs

| Guide | Description |
|-------|-------------|
| [API guide](./api.md) | Endpoints, request/response shapes |
| [Data model](./data-model.md) | Intelligence schema and relationships |
| [Collectors](./collectors.md) | DNS, HTTP, TLS, CT, RDAP collectors |
| [CT ingestor](./ct-ingestor.md) | Log ingestion, backfill, TLD filtering |
| [Pivots](./pivots.md) | Reverse intelligence via graph pivots |
| [Operations](./operations.md) | Deployment, env vars, tuning |
| [Metrics](./metrics.md) | Operational metrics and Prometheus |
| [OpenAPI spec](./openapi.yaml) | Machine-readable API schema |
