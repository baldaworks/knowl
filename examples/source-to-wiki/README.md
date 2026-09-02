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
├── concepts/
│   ├── authentication-and-session-security.md
│   ├── data-retention-and-lifecycle.md
│   └── incident-response-and-failover.md
└── entities/
    └── acme-cloud-platform.md
```

Every page contains exact provenance references linking back to the raw source file and immutable revision:

```yaml
---
type: topic
title: Authentication & Session Security
knowl:
  id: concepts/authentication-and-session-security
  source_refs:
    - wiki-filesystem:engineering-docs/authentication-service.md@72f58ee1af237dec1f3c78631782b5127f1b1e8748e2195d5f19c86137e7971c
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

### Configured Backend: Native Antigravity ACP

The workspace configuration in [`.config/knowl/config.yaml`](.config/knowl/config.yaml) configures the maintainer backend using native `antigravity_acp`:

```yaml
runtime:
  providers:
    antigravity:
      type: antigravity_acp
      antigravity_acp:
        model: gemini-3.7-flash-medium

knowl:
  provider: antigravity
  workspace:
    path: knowledge
  storage:
    type: sqlite
    sqlite:
      path: knowledge/.knowl/state.db
  scope: local
  sources:
    - id: engineering-docs
      type: filesystem
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
