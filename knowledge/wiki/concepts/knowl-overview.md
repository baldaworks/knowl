---
type: concept
title: Knowl Overview and Operating Modes
knowl:
  id: concepts/knowl-overview
  source_refs:
    - wiki-filesystem:knowl-docs/README.md@10d0fff017afc2a41b8399aacccd4b97683bf81e6b5575724f1af66b3eca9715
    - wiki-filesystem:knowl-docs/design.md@c9a981be23b39a137da77ad85726d8382d33306b44e801022bcdd4de4b525132
---
# Knowl Overview and Operating Modes

Knowl is a self-hosted LLM wiki for agentic applications that turns durable sources into an inspectable Markdown wiki with OKF-compatible storage (`okf-v0.2`) and returns bounded, provenance-backed evidence.

## Operating Modes and Baseline Use Cases

Knowl operates across two primary deployment modes, both backed by SQLite as the default operational store:

- **Local mode**: Maintains a project-local wiki through ACP (including Codex or custom ACP stdio maintainers).
- **Service mode**: Runs as a persistent MCP/HTTP service with dedicated storage. Go applications can also embed the same host runtime in-process using Fx.

Knowl supports five baseline use cases:

1. Bootstrap an existing Markdown wiki, Obsidian vault, or OKF v0.2 bundle into a Knowl-owned workspace as the first production filesystem-source sync.
2. Ingest supplied text or a URI reference through one canonical pipeline.
3. Retrieve bounded evidence and provenance for a host query.
4. Read durable ingest-operation status.
5. Allow operators to browse the current wiki, immutable accepted sources, retrieval evidence, and processing history through an optional read-only web UI.

### Host vs. Knowl Ownership and Non-Goals

- **Host responsibilities**: Decides which events (e.g., ADRs, completed stories, investigations, issues, pull requests, or runbooks from hosts like Balda or Norma) are durable, assigns immutable source revisions, owns session and user context, orchestrates tools, maps callers to the trusted Knowl scope (which public callers cannot override), and generates the final user answer. Knowl does not own external systems like Slack, Telegram, Jira, or GitHub.
- **Knowl responsibilities**: Owns durable acceptance, canonical `raw/` and `wiki/` Markdown artifacts, operation recovery, and bounded evidence with source references. See [Workspace Semantics and Source Lifecycle](workspace-and-source-lifecycle.md) and [Maintenance Contract and Execution](maintenance-contract-and-execution.md).
- **Explicit non-goals**: Knowl is not session, user-fact, or temporal memory; agent orchestration; the primary final-answer generator; a generic memory platform; or a multi-tenant control plane. It does not promise automatic crawling or research, vector DB as canonical storage, implicit forgetting, binary/image understanding, a broad CRUD/admin API, or automatic Git commit/push of the generated workspace.

## Public Business Contract and Usage Surfaces

Knowl exposes exactly three public business operations across MCP (primary agent-facing data plane) and HTTP/OpenAPI (`api/openapi/knowl.yaml`, deterministic control transport):

| Operation | MCP Tool | HTTP Endpoint |
| --- | --- | --- |
| Retrieve bounded evidence | `knowl_retrieve` | `GET /v1/retrieve` |
| Ingest one source | `knowl_ingest` | `POST /v1/ingest` |
| Read one durable operation status | `knowl_operation` | `GET /v1/operations/{operation_id}` |

Operational endpoints `GET /healthz` and `GET /readyz` are not business operations. Neither transport exposes direct page CRUD, raw workspace writes, search sub-steps, or public review/apply choreography.

### Optional Operator Web UI and Read API

- Adds four server-rendered screens (**Knowledge**, **Search**, **Operations**, **Sources**) and a separate operator read API (`api/openapi/operator.yaml`) with eight `GET` routes bound to the configured workspace and scope.
- Disabled by default; shares the existing HTTP listener when enabled.
- Requires the operator token, which also authorizes the existing agent write endpoints.
- Reads current canonical pages (whole-page references without claim-level attribution) and immutable accepted raw revisions. Search uses the existing retrieval path once per submitted query (sending the query to the configured embedding provider when hybrid search is enabled).
- Browsing never invokes a maintainer, fetches upstream sources, or allows browser mutations, chat, edit controls, or changeset review.
- Go templates and pinned local assets are embedded directly in the service binary, requiring no Node.js runtime or frontend build step for normal Go builds or library consumers.

### Supported Go Surface and Architecture Ownership

```text
Agent-facing data plane:     MCP
Deterministic host control:  HTTP/OpenAPI
Go in-process alternative:   Fx over the same runtime
Operator convenience:        cmd/knowl
Underlying business policy:  pkg/knowl/app
```

Supported Go imports for embedding:

- `pkg/knowl`: Plain-Go host composition root and host API (the only package that composes multiple adapters).
- `pkg/knowlfx`: Fx lifecycle integration.
- `pkg/knowl/mcp`: Bounded MCP adapter.
- `pkg/knowl/types`: Transport-neutral domain types and shared IDs (has no Knowl-package dependencies).

Dependencies flow strictly inward (`entrypoints and surfaces -> composition -> adapters -> app policy -> shared types`), enforced by `.go-arch-lint.yml`:

- `pkg/knowl/wiki`: Wiki and frontmatter semantics.
- `pkg/knowl/app`: Business policy and consuming ports.
- `content/fs`, `store/*`, `provider`: Adapters for workspace, operational state, and provider.
- `internal/httpapi`, `internal/mcphttp`: HTTP adapter and Streamable HTTP transport for MCP.
- `internal/source`: Filesystem and remote Git adapters, normalization, and reconciliation.
- `cmd/knowl`: Operator CLI convenience wrapper.

## Core Subsystems

- **Agent and Maintainer Integration**: See [Agent Maintainers and Provider Configuration](agent-maintainers.md) and [Maintenance Contract and Execution](maintenance-contract-and-execution.md).
- **Search and Retrieval**: See [Retrieval and Hybrid Search Architecture](retrieval-and-search.md).
- **Workspace, Sources, and Lifecycle**: See [Workspace Semantics and Source Lifecycle](workspace-and-source-lifecycle.md).

## Documentation Versioning Policy

Current documentation guides describe the checked-in source code. Recipes targeting a published release explicitly name that version, and historical release recipes do not imply that older binaries include newer features.
