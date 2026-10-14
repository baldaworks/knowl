---
type: topic
title: Execution Modes
knowl:
  id: concepts/execution-modes
  source_refs:
    - wiki-filesystem:knowl-docs/README.md@5dc9ddce38b85082810b9f03e88921ba503abaebd974be1dacb48fde1069a10d
    - wiki-filesystem:knowl-docs/local.md@b72ed8a3f8d255df1843e32445e50fa7be99e752b764711b7494cd76f325c5e0
    - wiki-filesystem:knowl-docs/service.md@646948819ed449ec0af05909f13981d098c69e75cd4e7c9b09749f4c76799754
    - wiki-filesystem:knowl-docs/web-ui.md@fdf8776452fa7a187b2ab02c1f9c637d49472fa460390a57f0da9a30de5e3a41
---
# Execution Modes

Knowl supports two operational execution modes that generate a Markdown knowledge base from configured sources and enable evidence retrieval: **Local mode** and **Service mode**.

For overall system capabilities, interfaces, and package layering, see [Architecture](architecture.md).

## Local Mode
Local mode runs Knowl directly within a project repository using one-shot CLI commands and an ACP maintainer (such as Codex), eliminating the need for persistent service daemons or operator authentication tokens.

### Configuration
Local mode is configured in `.config/knowl/config.yaml`:
```yaml
runtime:
  providers:
    codex:
      type: codex_acp
      codex_acp:
        bridge_version: "1.7.3"
knowl:
  provider: codex
  workspace:
    path: knowledge
  storage:
    type: sqlite
    sqlite:
      path: .knowl/knowl.sqlite
  scope: local
  server:
    listen_addr: 127.0.0.1:0
  sources:
    - id: engineering-docs
      type: filesystem
      filesystem:
        root: sources
        include: ["authentication-service.md"]
        flavor: markdown
```

- **Scope & Storage**: Operates under `scope: local` with SQLite storage (defaulting to `.knowl/knowl.sqlite`).
- **Workspace**: Maintains the semantic Markdown wiki under `knowledge/wiki/` and preserves immutable accepted source revisions under `knowledge/raw/`.
- **Maintainer**: Uses an ACP maintainer provider (e.g., `codex_acp` with a pinned `bridge_version: "1.7.3"` and optional `model` override) executing over local stdio.
- **Server**: Sets `server.listen_addr: 127.0.0.1:0` for local execution without exposing network listeners.

### Lifecycle and CLI Workflow
Local execution relies on sequential CLI commands:
- `knowl init`: Initializes project state and directory layout.
- `knowl run --source <source_id>`: Synchronizes source revisions and executes maintainer wiki updates.
- `knowl validate`: Checks required workspace paths and validates the root index and log controls.
- `knowl source status <source_id>`: Inspects sync status; maintenance must be completed rather than just scanned.
- `knowl retrieve '<query>'`: Retrieves bounded evidence containing matching page identifiers, excerpts, and provenance source references.

## Service Mode
Service mode runs Knowl as a persistent MCP and HTTP service with dedicated operational storage and multi-client retrieval capabilities.

### Deployment and Configuration
Service mode is typically deployed using Docker Compose (`deploy/sidecar/compose.yaml`):
- **Environment Variables**: Requires `OPENAI_API_KEY`, `OPENAI_MODEL`, and `KNOWL_OPERATOR_TOKEN` (used for bearer authentication).
- **Execution & Storage**: Runs as a non-root user, listening on port 8080 inside Docker and publishing only to host loopback (`127.0.0.1:8080`). It owns `/var/lib/knowl/knowledge` with SQLite database storage under `.knowl/` persisted across restarts in the `knowl-data` named volume. Sources (e.g., `/sources/engineering`) are mounted read-only.
- **Maintainer**: Uses a configured provider (such as the OpenAI maintainer provider) or an ACP agent executable supplied inside the container.

### Client Interfaces
- **HTTP / REST API**:
  - Evidence retrieval: `GET /v1/retrieve` with Bearer token authentication in the `Authorization` header and query parameter `query`.
  - Ingestion: `POST /v1/ingest`.
  - Status: `GET /v1/operations/{operation_id}`.
- **Streamable HTTP MCP**:
  - Connects at `http://127.0.0.1:8080/mcp` using `transport: streamable_http` and Bearer token headers.
  - Exposes `knowl_retrieve`, `knowl_ingest`, and `knowl_operation` tools.
- **Bundled Web UI**:
  - Enabled via `knowl.web.enabled: true` and authenticated using `knowl.operator.token`.
  - Accessible at `http://127.0.0.1:8080/ui/` with four server-rendered screens powered by bundled local assets without Node or frontend build tooling:
    - **Knowledge**: Renders the canonical wiki root, catalog links, breadcrumbs, an expandable directory file tree ("All pages"), page metadata ("Details"), and immutable accepted text inspection ("Page sources" / "Read saved source").
    - **Search**: Interactive full-wiki search indicating embedding provider status, displaying ranked evidence cards with retrieval badges (`lexical`, `hybrid`, `degraded`, `failed`), and supporting JSON export.
    - **Operations**: Stored task history filtered by status and source ID, detailing execution, retry status, maintainer context fitting, plan counts, and correction reports.
    - **Sources**: Lists configured sources and documents tracking upstream heads, accepted revisions, processing revisions, and synchronization history.

### Health and Operations
- **Health Endpoints**:
  - `/healthz`: Indicates the HTTP process is running.
  - `/readyz`: Verifies operational storage, recovery status, and retrieval readiness.
- **Maintenance Status**: Checked via container CLI execution: `knowl --config-dir /etc source status <source_id>`.
- **Persistence**: Volumes retain `raw/` history, `wiki/` markdown bundles, source statuses, and operational recovery state.
