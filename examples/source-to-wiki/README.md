# Showcase: Source -> Wiki

This directory demonstrates Knowl's core knowledge pipeline: turning raw, disparate project documentation (`sources/`) into a structured, queryable Open Knowledge Format (OKF) Markdown wiki (`wiki/`).

```text
sources/ (raw markdown)  ──>  knowl run  ──>  wiki/ (semantic entities, catalogs, search index)
```

---

## 1. Input: The Raw Sources (`sources/`)

Project documentation starts as unstructured or semi-structured files:

- [`sources/architecture-overview.md`](sources/architecture-overview.md) — System services (API Gateway, Auth, Orders), message bus, and storage layers.
- [`sources/authentication-service.md`](sources/authentication-service.md) — OAuth2/OIDC, JWT tokens (15m expiry), refresh tokens, and Redis Pub/Sub session revocation.
- [`sources/database-retention-policy.md`](sources/database-retention-policy.md) — 2-year active retention, parquet archiving, WORM audit logs, and GDPR cascading deletes.
- [`sources/incident-response-runbook.md`](sources/incident-response-runbook.md) — SEV-1 triage, Patroni PostgreSQL failover sequence, and replica promotion.

---

## 2. Output: The Generated Wiki (`wiki/`)

Knowl organizes these inputs into an **Open Knowledge Format (OKF)** Markdown wiki. Notice the clean, browseable structure checked in directly under `wiki/`:

```text
wiki/
├── index.md                              # Root catalog linking the knowledge base
├── catalogs/
│   └── engineering/
│       └── index.md                      # Engineering catalog
└── entities/
    ├── architecture-overview.md          # Normalized entity page with OKF frontmatter
    ├── authentication-service.md         # Normalized entity page with OKF frontmatter
    ├── database-retention-policy.md      # Normalized entity page with OKF frontmatter
    └── incident-response-runbook.md      # Normalized entity page with OKF frontmatter
```

Every page contains exact provenance references linking back to the raw source file and immutable revision:

```yaml
---
id: entities/authentication-service
title: Authentication Service
type: entity
version: "0.2"
parent: catalogs/engineering
source_refs:
  - wiki-filesystem:engineering-docs/authentication-service.md
---
```

---

## 3. The One-Shot Run Workflow (`run.sh` / `knowl run`)

Instead of requiring an ongoing daemon or writing custom integration code, you execute the one-shot run workflow:

```bash
./run.sh
```

Or directly using the `knowl` binary from this directory:

```bash
knowl run
```

### Configured Backend: ACP via `acprun`

The workspace configuration in [`.config/knowl/config.yaml`](.config/knowl/config.yaml) configures the maintainer backend using `acprun` with `antigravity-acp`:

```yaml
runtime:
  providers:
    antigravity:
      type: generic_acp
      generic_acp:
        cmd:
          - acprun
          - antigravity-acp

knowl:
  provider: antigravity
  workspace:
    path: .
  storage:
    type: sqlite
    sqlite:
      path: .knowl/state.db
  scope: local
  sources:
    - id: engineering-docs
      type: filesystem
      enabled: true
      config:
        filesystem:
          root: sources
          include:
            - "**/*.md"
          flavor: markdown
```

---

## 4. Deterministic Verification

Run the automated integration test to verify the workspace, sources, wiki structure, and search retrieval:

```bash
go test -v ./examples/source-to-wiki
```
