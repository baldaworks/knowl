---
type: topic
title: Sidecar Deployment
knowl:
  id: concepts/sidecar-deployment
  source_refs:
    - wiki-filesystem:knowl-docs/sidecar.md@17c5d32a0547e501b796e12127226b2585fd3dda405936755f1b4b7591a02e8b
    - wiki-filesystem:knowl-docs/releases/v0.1.0.md@183d6c7f742a0555fa5f792ea5d84435ede6851d29fb5007301c8e8144bfb561
---
# Sidecar Deployment

Knowl's baseline production deployment model is a self-hosted sidecar service running alongside an agentic application container.

## Architecture and Storage

- **Persistent Volume**: Knowl requires persistent storage mounted at `/var/lib/knowl` to retain raw source history, tombstones, recovery journals, and SQLite state.
- **Workspace Layout**: The container owns `/var/lib/knowl/knowledge` as the canonical Open Knowledge Format (OKF) workspace and `/var/lib/knowl/knowledge/.knowl/knowl.sqlite` as the default SQLite operational database.
- **Storage Isolation**: Authoritative source roots must be mounted separately and read-only (e.g., `/sources/engineering:ro`). The agent must not mount or directly mutate Knowl's workspace.

## Container Lifecycle

- **Startup**: On container launch, Knowl runs `knowl --config-dir /etc init` followed by `knowl --config-dir /etc start`, initializing empty persistent volumes before listening on `0.0.0.0:8080`.
- **Provider Requirement**: Running containers require a valid maintainer provider configured via `runtime.providers` and selected with `knowl.provider`. Startup halts before readiness if no provider is present.
- **Source Synchronization**: Configured sources synchronize periodically and on start (when `sync.on_start` is enabled) with bounded retries. Source sync accepts immutable raw revisions and queues asynchronous maintenance operations without blocking service readiness.

## Health and Service Contracts

- **Liveness and Readiness**: `GET /healthz` indicates the HTTP server is running, while `GET /readyz` verifies workspace recovery, durable-work resumption setup, store readiness, and search projections.
- **Agent Interfaces**: Neighboring agents interact with the sidecar over loopback via MCP Streamable HTTP at `/mcp` or deterministic HTTP endpoints (`/v1/retrieve`, `/v1/ingest`, `/v1/operations/{operation_id}`).

## Upgrade, Rollback, and Execution Guarantees

- **Durable Work Resumption**: Accepted sources and execution descriptors survive restart; work executes with at-least-once semantics and idempotent canonical commit.
- **Non-Destructive Rollback**: During rollback or upgrades, retain `/var/lib/knowl` rather than deleting volume state or running destructive down migrations. Additive operational schemas are preserved for forward recovery.
- **Production Artifact Pinning**: Production deployments pin published immutable manifest digests (`ghcr.io/baldaworks/knowl@sha256:...`) instead of mutable release tags.
