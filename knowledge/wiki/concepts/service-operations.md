---
type: topic
title: Service Operations
knowl:
  id: concepts/service-operations
  source_refs:
    - wiki-filesystem:knowl-docs/local-codex.md@2ba53250fc8e94bf95fb84e0c188a320f5670719b4daddf61ce2cfa8557dbafa
    - wiki-filesystem:knowl-docs/operations.md@2054c49f67e051de123d967fc81a984ff96292c970a215f1d0e86dd1516649ad
    - wiki-filesystem:knowl-docs/sidecar.md@16a5d0b29e4d14c9d1f6b5e23df89b50f47de8a6cb2555a10ca5ff24a7605ccf
    - wiki-filesystem:knowl-docs/testing.md@9b27371ec8377d553b279f4be2f1304defc9e8b316ad2186379d24311dd85724
    - wiki-filesystem:knowl-docs/web-ui.md@87191e228d60139852c21653b38d3d02d1659893279ab85a9e9b0b51fef6aa85
---
# Service Operations

Knowl operates as a standalone knowledge service providing canonical workspace management, a unified ingest pipeline, operational state projections, and MCP/HTTP interfaces.

## Runtime and Deployment

- **Service/Sidecar Deployment**: Baseline production mode mounting `/var/lib/knowl` with canonical workspace at `/var/lib/knowl/knowledge`.
- **In-Process Embedding**: Go applications can embed the runtime directly via root `pkg/knowl` or with Uber Fx lifecycle integration via `pkg/knowlfx.NewApp`.
- **Local Codex Integration**: Connects a project's Knowl wiki to Codex through MCP stdio without a background daemon or separate service. Codex launches `npx --yes @baldaworks/knowl@0.6.0 mcp stdio` directly with the active project as its working directory.
- **Liveness and Readiness**:
  - `GET /healthz`: Indicates the process is serving HTTP.
  - `GET /readyz`: Confirms workspace recovery, store setup, and projections are ready.

## Maintenance Workers

- **Execution Capacity**: Configured via `knowl.workers` (accepts integer `1` or `2`, default `1`). Setting to `2` allows concurrent maintenance operations to infer while another waits for provider responses.
- **Execution Owners**: Each worker owns an independent maintainer runtime and session. The shared publication gate serializes canonical commits, projections, and outcomes.
- **Shutdown and Drain**: Process termination first closes admission and new claims, drains active owners within the shutdown deadline, and retains unresolved resources for cleanup.

## Container and Sidecar Operations

- **Startup Workflow**: On launch, the container executes `knowl --config-dir /etc init` followed by `knowl --config-dir /etc start`, initializing a valid workspace on first boot and binding to `0.0.0.0:8080`.
- **Volume Ownership & Mount Boundaries**:
  - Persistent storage mounts at `/var/lib/knowl`, containing canonical workspace `/var/lib/knowl/knowledge` and SQLite operational store `/var/lib/knowl/knowledge/.knowl/knowl.sqlite`.
  - Host applications/agents must not be mounted directly into Knowl's workspace and must not mutate `wiki/**` directly.
  - Authoritative source directories must be mounted separately and read-only (e.g., `-v /path/to/source:/sources/source:ro`).
  - Persistent volumes must retain the full `/var/lib/knowl` directory so raw history, source status, tombstones, recovery journals, and SQLite state survive container restarts.
- **Container Build & Metadata**: Production images build via root `Dockerfile` using build arguments `VERSION`, `REVISION`, and `CREATED`, run non-root, and include standard OCI metadata. Local persistence and auth can be verified via `scripts/smoke-test-sidecar.sh`.
- **Direct & Compose Execution**: Direct container run binds published ports to loopback (`-p 127.0.0.1:8080:8080`). Standard compose setup is provided via `deploy/sidecar/compose.yaml`, with `KNOWL_IMAGE` allowing prebuilt image overrides. Production deployments should pin immutable manifest digests.
- **Source Sync on Start**: Each source configures `sync.on_start` explicitly. Source sync failures do not make the service unready or discard prior snapshots.
- **Container Web Access**: The optional web UI is built into current images with Go templates and local assets (no Node or CDN dependencies). Container overlay configures `web.enabled: true`, `server.listen_addr: 0.0.0.0:8080`, and `operator.token: ${KNOWL_OPERATOR_TOKEN}`. Host operator secrets must be explicitly passed into Docker/Compose or mounted as local config overrides under `/etc/knowl/`.

## Configuration

Configuration is loaded by default from `.config/knowl/config.yaml`, selectable via `--config-dir` and `--profile`:
- `runtime:` Configures the shared provider registry in typed shape (e.g., `opencode` with model specification).
- `knowl:` Configures application settings, including:
  - `provider`: Selects an entry from `runtime.providers` (host fails before readiness if missing or invalid).
  - `workers`: Execution capacity (`1` or `2`).
  - `output.max_corrections`: Bounded output correction limit (`0` or `1`, default `1`).
  - `workspace.path`: Path to workspace (defaults to `.`).
  - `storage.type`: Chooses `sqlite` (default, `sqlite.path: .knowl/knowl.sqlite`) or `postgres` (`postgres.dsn: ${KNOWL_POSTGRES_DSN}`).
  - `server.listen_addr`: Listening address (default `127.0.0.1:8080`).
  - `web.enabled`: Controls optional web UI and operator read endpoints (boolean, default `false`).
  - `operator.token`: When non-empty, requests to `/v1/*`, `/mcp`, and operator endpoints require `Authorization: Bearer <token>`; probes remain unauthenticated. Required when web UI is enabled.
  - `sources`: Configures filesystem and Git sources:
    - **Filesystem Sources**: `type: filesystem`, `filesystem.root`, `filesystem.include`, `filesystem.flavor` (`obsidian`, `markdown`, `okf`), and `sync` settings.
    - **Git Sources**: `type: git`, `git.remote`, `git.ref`, `git.ref_kind: branch`, `git.include`, `git.flavor`, `git.uri_base`, and authentication (`auth.secret_env` or `auth.key_file`). Managed via a bare cache under `<workspace>/.knowl/cache/git/<source-id>` with default 500 MiB transfer and 512 MiB cache limits.

Common environment variable overrides include `KNOWL_PROVIDER`, `KNOWL_WORKSPACE_PATH`, `KNOWL_STORAGE_TYPE`, `KNOWL_STORAGE_SQLITE_PATH`, `KNOWL_STORAGE_POSTGRES_DSN`, `KNOWL_SERVER_LISTEN_ADDR`, and `KNOWL_OPERATOR_TOKEN`.

## Optional Web UI and Operator Reads

- **Activation**: Enabled by setting `knowl.web.enabled: true` and providing `knowl.operator.token`. Embedded applications use `Config.Web.Enabled` and `Config.OperatorToken`. MCP stdio explicitly disables web access.
- **Web Shell & Authentication**: UI connects at `http://127.0.0.1:8080/ui/`. Public shell and assets contain no workspace data; `/ui/fragments/*` and `/operator/v1/*` require bearer authentication. Tokens remain in browser memory for the document session, never stored in cookies, local storage, or URLs. Responses set `Cache-Control: no-store`.
- **Network Boundary**: Recommended for loopback. Remote access requires terminating HTTPS at a trusted reverse proxy forwarding the bearer header without logging credentials or query parameters. Direct UI browsing loads assets locally and avoids external requests; deliberate clicks on external links do not forward bearer credentials.
- **Operator Read API**: Exposes seven GET routes (`/operator/v1/catalogs`, `/operator/v1/pages`, `/operator/v1/page`, `/operator/v1/source-revision`, `/operator/v1/sources`, `/operator/v1/sources/{source_id}`, `/operator/v1/operations`). Pagination defaults to 50 items (limit 1–100, Operations UI starts at 10) with opaque 8 KiB restart-invalidated cursors. Query strings exceeding 16 KiB or duplicate parameters fail closed.
- **Screen Workflows**:
  - **Knowledge**: Renders the canonical root and actual `index.md` body. Follows verified catalog links and breadcrumbs without inferring deeper file-path ancestry. Includes an **All pages** expandable directory tree with on-demand child loading and bounded in-memory state restored on browser Back. An expandable **Details** panel displays page metadata, while **Page sources** allows reading immutable accepted source text via **Read saved source** without upstream fetches. **Open original** appears only when provenance contains allowed credential-free HTTP/HTTPS URLs.
  - **Search**: Executes standard retrieval once per explicit submission (no live search on typing). Displays server-ordered evidence with original snippets and retrieval diagnostic badges (lexical, hybrid, degraded, failed). Supports **View JSON** and **Export JSON** without credentials; browser Back restores query and results without repeating retrieval.
  - **Operations**: Lists processing history with status and source filters. Detail views reveal execution/retry facts, retrieval reports, context fitting budgets, plan summaries, and correction outcomes. Polling refreshes queued/running operations while tabs are visible, pausing in hidden tabs and halting on terminal status.
  - **Sources**: Displays configured sources and document inventories (upstream head, accepted revision, processing revision) with links to associated operations and processing history.

## Optional Embeddings and Hybrid Retrieval

- **Configuration**: Opt-in via `knowl.embeddings` specifying `enabled: true`, OpenAI-compatible `endpoint`, `model`, immutable `revision`, `dimensions` (1–4,096), `query_prefix`, `passage_prefix`, and `failure_policy` (`lexical` or `strict`).
- **CPU Reference TEI Stack**: Optional CPU embeddings can run via `deploy/sidecar/embeddings.compose.yaml` deploying linux/amd64 CPU TEI 1.9.0 with E5-base revision `d128750597153bb5987e10b1c3493a34e5a4502a` returning 768 normalized dimensions. Models are cached in persistent volume `tei-model-cache`. Knowl restarts rebuild dense state after an initial degraded startup once TEI is healthy.
- **Inference & Chunking Bounds**: 384 runes per chunk with 64 overlap; up to 16 chunks per page and 4 per query. Client requests bound to 10 seconds with at most 16 inputs (2,048 bytes each).
- **Projection Limits**: Scoped projection capped at 8,192 chunks and 64 MiB. Failed projections under `failure_policy: lexical` fall back to lexical retrieval with `degraded` effective mode.

## Operator CLI Workflows

- **Initialization & Validation**: `knowl init`, `knowl validate`, `knowl start`.
- **Workspace Adoption**: `knowl bootstrap wiki <path>`, `knowl bootstrap obsidian <path>`, or `knowl bootstrap okf <path>` (bootstrap remains optional).
- **Migration**: `knowl migrate okf-v0.2` converts legacy workspaces idempotently.
- **One-Shot Execution**: `knowl run` executes sync, drains maintenance operations, and reconciles hierarchy without a background daemon (supports `--source <id>`, `--no-sync`, `--no-hierarchy`).
- **Source Synchronization**: `knowl source list`, `knowl source sync <id>`, `knowl source sync --all`, and `knowl source status <id>`.
- **Hierarchy Reconciliation**: `knowl hierarchy reconcile` triggers subject-first OKF catalog organization under durable planner identity `hierarchy-v3`.

## Local Codex Plugin and Workflow

- **Setup and Installation**: Initialized from project root using `npx --yes @baldaworks/knowl@0.6.0 setup codex`. Setup creates missing project state (`.config/knowl`, `schema.md`, `raw/`, `wiki/`, and `.knowl/`), validates workspace structure, and installs the release-matched Knowl marketplace and plugin. Conflicting installations require explicit approval and rerun with `--replace`.
- **Prerequisites**: Node.js with npm/npx, Codex CLI, and an installed and authenticated `opencode acp` runtime for maintenance.
- **Workflow Skills**: Codex provides two dedicated workflow skills:
  - `$knowl:setup`: Repeats or repairs the pinned setup workflow.
  - `$knowl:run`: Validates, executes one bounded `knowl run`, validates again, and reports structured results under Knowl-owned paths. Uses local `knowl` binary only when `knowl version --json` identifies exact `0.6.0`; otherwise falls back to `npx --yes @baldaworks/knowl@0.6.0`.
- **MCP Connection**: Codex starts and owns `npx --yes @baldaworks/knowl@0.6.0 mcp stdio` with the active project as working directory without daemon or port allocation.
- **Platforms and Offline Prewarming**: Supports macOS (x64/arm64), Linux (x64/arm64), and Windows (x64). Offline environments require prewarming the npm package and sparse Git marketplace checkout.
- **Scope**: Supported exclusively for local Codex projects on a local filesystem; excludes hosted Codex, remote MCP hosting, shared/network filesystems, and secondary workspace folders.

## Maintenance Scheduling and Failure Recovery

Source sync stores immutable evidence in `raw/` and reserves maintenance operations asynchronously.
- **Operation Counts**: `knowl source status` tracks counts and samples for `queued`, `retrying`, `replayed`, `committed`, and `failed`.
- **Automatic Retries**: Retries up to 3 total work attempts with exponential backoff (starting at >= 30 seconds, maximum 5 minutes, bounded positive jitter). Undecodable or invalid provider outputs fail permanently.
- **Bounded Output Correction**: Operates within a single work attempt using `knowl.output.max_corrections` (0 or 1). Shared budget is 1 MiB output and 5 minutes total planning time. Rejection triggers typed feedback (`structured_output_invalid`, `source_plan_invalid`, `hierarchy_plan_invalid`).
- **Manual Recovery**: `./knowl source retry <id> --failure-class <class> [--dry-run]` allows previewing and requeuing failed operations by class (`provider`, `source`, `staging`, `maintenance_policy`) after resolving root causes.
- **Operation Details Inspection**: `GET /v1/operations/{id}` and MCP `knowl_operation` return structured diagnostic `details` covering attempt context snapshots, budget utilization, candidate dispositions, retrieval reports, staged plan summaries, and correction outcomes.

## Testing and Quality Verification

- **Standard Verification Suite**:
  - Full native tests: `go test ./...`
  - Linter: `go tool golangci-lint run ./...`
  - Architecture lint: `go run github.com/fe3dback/go-arch-lint@v1.15.0 check --project-path .`
  - Integration store tests: `go test -tags integration -count=1 ./pkg/knowl/store/postgres -run TestStoreContractWithTestcontainers`
- **Context & Maintenance Baseline (`context-baseline-v1`)**:
  - Run via `go test -count=1 -json ./pkg/knowl/... -run 'TestContextBaseline|TestIngestRejectsStaleReviewedPlan' > /tmp/knowl-context-baseline.jsonl`.
  - Evaluates retrieval accuracy (exact/base-form/mixed queries), source signal title extraction, catalog scaling (up to 32 catalogs), provider input fitting within strict 4 MiB limits, and concurrency claims.
- **CPU Embedding Quality Evaluation**:
  - Executed via `go test -tags integration -count=1 -v ./pkg/knowl/store/eval -run 'TestQualityCorpus|TestRealCPUModel'` with `KNOWL_EMBEDDING_EVAL_ENDPOINT`, `KNOWL_EMBEDDING_EVAL_POSTGRES_DSN`, and `KNOWL_EMBEDDING_EVAL_OUTPUT` configured.
  - Verified against pinned `intfloat/multilingual-e5-base` revision `d128750597153bb5987e10b1c3493a34e5a4502a` (768 dimensions), passing 13/13 query/source top-five cases and 12/12 golden corpus across SQLite and PostgreSQL.
- **Execution and Concurrency Gates**:
  - Evaluated via `go test -count=2 -json ./pkg/knowl ./pkg/knowl/app -run 'TestContextBaselineBlockedExecution|TestHostComposesDistinctRuntimeSessions|TestConcurrentPreparedSourcePlansFailSafely|TestOverlappingSourceAndHierarchyPreserveFirstCommit'`.
  - Enforces serial execution under capacity 1, parallel progress under capacity 2, and typed conflict preservation for concurrent overlapping plans without automatic replanning.

See [[concepts/architecture]] for system design, [[concepts/public-contract]] for service APIs, and [[concepts/content-and-trust-boundaries]] for storage and security models.
