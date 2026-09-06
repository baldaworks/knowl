---
type: topic
title: Service Operations
knowl:
  id: concepts/service-operations
  source_refs:
    - wiki-filesystem:knowl-docs/operations.md@10058de9f9043ad8f77024a0347c43e0435c0f7c0c417df1e538afc676b4e7d7
    - wiki-filesystem:knowl-docs/sidecar.md@17c5d32a0547e501b796e12127226b2585fd3dda405936755f1b4b7591a02e8b
---
# Service Operations

Knowl operates as a standalone knowledge service providing canonical workspace management, a unified ingest pipeline, operational state projections, and MCP/HTTP interfaces.

## Runtime and Deployment

- **Service/Sidecar Deployment**: Baseline production mode mounting `/var/lib/knowl` with canonical workspace at `/var/lib/knowl/knowledge`.
- **In-Process Embedding**: Go applications can embed the runtime directly via root `pkg/knowl` or with Uber Fx lifecycle integration via `pkg/knowlfx.NewApp`.
- **Liveness and Readiness**:
  - `GET /healthz`: Indicates the process is serving HTTP.
  - `GET /readyz`: Confirms workspace recovery, store setup, and projections are ready.

## Container and Sidecar Operations

- **Startup Workflow**: On launch, the container executes `knowl --config-dir /etc init` followed by `knowl --config-dir /etc start`, initializing a valid workspace on first boot and binding to `0.0.0.0:8080`.
- **Volume Ownership & Mount Boundaries**:
  - Persistent storage mounts at `/var/lib/knowl`, containing canonical workspace `/var/lib/knowl/knowledge` and SQLite operational store `/var/lib/knowl/knowledge/.knowl/knowl.sqlite`.
  - Host applications/agents must not be mounted directly into Knowl's workspace and must not mutate `wiki/**` directly.
  - Authoritative source directories must be mounted separately and read-only (e.g., `-v /path/to/source:/sources/source:ro`).
  - Persistent volumes must retain the full `/var/lib/knowl` directory so raw history, source status, tombstones, recovery journals, and SQLite state survive container restarts.
- **Container Build & Metadata**:
  - Service images build via root `Dockerfile` using build arguments `VERSION`, `REVISION`, and `CREATED`.
  - Production images run as a non-root user and include OCI metadata (source, revision, version, license, creation timestamp).
  - Local verification can be performed with `scripts/smoke-test-sidecar.sh knowl:local`.
- **Direct & Compose Execution**:
  - Direct container run binds published ports to loopback (`-p 127.0.0.1:8080:8080`) to keep agent traffic host-local.
  - Standard compose setup is provided via `deploy/sidecar/compose.yaml` (customizable via `KNOWL_IMAGE`). Production deployments should pin immutable image manifest digests.

## Configuration

Configuration is loaded by default from `.config/knowl/config.yaml`, selectable via `--config-dir` and `--profile`. Baseline container config is defined in `deploy/sidecar/knowl.yaml`. It contains two top-level sections:
- `runtime:` Configures the shared provider registry (e.g., `opencode` with model specification).
- `knowl:` Configures application settings, including:
  - `provider`: Selects an entry from `runtime.providers` (host fails before readiness if missing or invalid).
  - `workspace.path`: Path to workspace (defaults to `.`).
  - `storage.type`: Chooses `sqlite` (default, `sqlite.path: .knowl/knowl.sqlite`) or `postgres` (`postgres.dsn: ${KNOWL_POSTGRES_DSN}`).
  - `server.listen_addr`: Listening address (default `127.0.0.1:8080`).
  - `operator.token`: When non-empty, requests to `/v1/*` and `/mcp` require an `Authorization: Bearer <token>` header; probe endpoints (`/healthz`, `/readyz`) remain unauthenticated.
  - `sources`: List of configured filesystem sources defining `id`, `type: filesystem`, `filesystem.root`, `filesystem.include`, `filesystem.flavor` (`obsidian`, `markdown`, `okf`), and `sync` settings (`on_start`, `interval`, `retry_initial`, `retry_maximum`). `sync.on_start` is explicit per-source configuration.

Common environment variable overrides include `KNOWL_PROVIDER`, `KNOWL_WORKSPACE_PATH`, `KNOWL_STORAGE_TYPE`, `KNOWL_STORAGE_SQLITE_PATH`, `KNOWL_STORAGE_POSTGRES_DSN`, `KNOWL_SERVER_LISTEN_ADDR`, and `KNOWL_OPERATOR_TOKEN`.

## Operator CLI Workflows

- **Initialization & Validation**: `knowl init`, `knowl validate`, `knowl start`.
- **Workspace Adoption**: `knowl bootstrap wiki <path>`, `knowl bootstrap obsidian <path>`, or `knowl bootstrap okf <path>` (bootstrap remains optional).
- **Migration**: `knowl migrate okf-v0.2` converts legacy workspaces idempotently.
- **One-Shot Execution**: `knowl run` executes sync, drains maintenance operations, and reconciles hierarchy without a background daemon (supports `--source <id>`, `--no-sync`, `--no-hierarchy`).
- **Source Synchronization**: `knowl source list`, `knowl source sync <id>`, `knowl source sync --all`, and `knowl source status <id>`.
- **Hierarchy Reconciliation**: `knowl hierarchy reconcile` triggers subject-first OKF catalog organization under durable planner identity `hierarchy-v3`.

## Maintenance Scheduling and Failure Recovery

Source sync stores immutable evidence in `raw/` and reserves maintenance operations asynchronously.
- **Operation Counts**: `knowl source status` tracks counts and samples for `queued`, `retrying`, `replayed`, `committed`, and `failed`.
- **Automatic Retries**: Retries up to 3 total work attempts with exponential backoff (starting at >= 30 seconds, maximum 5 minutes, bounded positive jitter). Undecodable or invalid provider outputs fail permanently.
- **Failure Isolation**: A failed source synchronization does not mark the service unready or discard prior successful snapshots.
- **Manual Recovery**: `./knowl source retry <id> --failure-class provider [--dry-run]` allows previewing and requeuing failed operations by class after resolving root causes.

See [[concepts/architecture]] for system design, [[concepts/public-contract]] for service APIs, and [[concepts/content-and-trust-boundaries]] for storage and security models.
