---
type: topic
title: Service Operations
knowl:
  id: concepts/service-operations
  source_refs:
    - wiki-filesystem:knowl-docs/operations.md@763e4b85a576fe30658e99c04960c712306d1f2dfa94aef06bc8fe5371766099
---
# Service Operations

Knowl is operated as a standalone knowledge service or embedded via Fx, providing unified configuration, operator CLI workflows, and deterministic failure recovery.

## Runtime Configuration

- **Configuration Schema**: Default configuration resides at `.config/knowl/config.yaml` with top-level sections for `runtime:` (shared provider registry) and `knowl:` (application settings).
- **Provider Binding**: `knowl.provider` selects an active maintainer from `runtime.providers`. A runnable host requires a configured provider before readiness (`/readyz`).
- **Storage Engines**: Storage defaults to SQLite (`knowl.storage.sqlite.path`) with optional PostgreSQL support via DSN (`knowl.storage.postgres.dsn`).
- **Authentication**: When `knowl.operator.token` is configured, `/v1/*` and `/mcp` endpoints require bearer authentication (`Authorization: Bearer <token>`). Health probes (`/healthz`, `/readyz`) remain unauthenticated.

## Operator Lifecycle and Workflows

- **Bootstrap**: Local repositories or vaults can be ingested via `knowl bootstrap {wiki|obsidian|okf} <path>`, executing a single sync to initialize workspace evidence without altering existing configs.
- **Batch Execution**: `knowl run` executes a complete one-shot processing cycle (syncing sources, draining queued maintenance, and reconciling OKF hierarchy) without running a background daemon.
- **Hierarchy Reconciliation**: `knowl hierarchy reconcile` invokes the `hierarchy-v3` subject-first planner to reorganize `wiki/index.md` and `wiki/catalogs/**/index.md` within strict resource and depth bounds.

## Maintenance Scheduling and Recovery

- **Durable Retry Scheduler**: Transient provider or transport failures undergo up to three work attempts. Retries use exponential backoff starting at 30 seconds with bounded positive jitter, capped at 5 minutes.
- **Terminal Failure Recovery**: Unrecoverable failures remain terminal until an operator explicitly reviews and requeues them using `knowl source retry <source-id> --failure-class <class> [--dry-run]`.
- **Operation State Tracking**: Source status tracks bounded counts and samples for `queued`, `retrying`, `replayed`, and `failed` maintenance tasks without exposing sensitive prompts or credentials.
