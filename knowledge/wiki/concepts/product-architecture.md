---
type: topic
title: Product Architecture
knowl:
  id: concepts/product-architecture
  source_refs:
    - wiki-filesystem:knowl-docs/design.md@d66ede26731cdde0aa980ac830f209706a8af8850fb45b0fb906d37a6dadc8d7
---
# Product Architecture

Knowl is a self-hosted knowledge sidecar designed for agentic applications, transforming durable source revisions into an inspectable Markdown knowledge base while returning bounded, provenance-backed evidence.

## Business and Operational Contracts

- **Public Operations**: The business interface provides exactly three operations exposed identically over MCP and HTTP:
  - `knowl_retrieve` (`GET /v1/retrieve`): Query bounded evidence with provenance.
  - `knowl_ingest` (`POST /v1/ingest`): Ingest a text or URI source through the canonical ingest pipeline.
  - `knowl_operation` (`GET /v1/operations/{operation_id}`): Inspect durable operation status.
- **Operational Probes**: `GET /healthz` verifies HTTP process liveness; `GET /readyz` verifies complete recovery, storage readiness, and search projections.
- **Integration Surfaces**: Supported integration boundaries include MCP Streamable HTTP for agent clients, HTTP/OpenAPI for deterministic host control, Go Fx embedding for in-process runtimes, and the `cmd/knowl` CLI for operator convenience.

## Architectural Layers and Inward Dependency Flow

Knowl enforces strict inward dependency flow (`surfaces -> composition -> adapters -> app policy -> shared types`):

- `pkg/knowl/types`: Shared domain identifiers and data shapes (zero internal Knowl dependencies).
- `pkg/knowl/wiki`: Wiki file parsing, frontmatter schemas, and Open Knowledge Format (OKF) semantics.
- `pkg/knowl/app`: Core business policy and port interfaces.
- Adapters (`content/fs`, `store/*`, `provider`, `pkg/knowl/mcp`, `internal/httpapi`, `internal/source`): Implement storage, transport, and synchronization ports.
- `pkg/knowl`: Central composition root and Host engine.
- `pkg/knowlfx` and `cmd/knowl`: In-process Fx module and CLI entrypoints.

## Search and Ranking Projections

- **Rebuildable Search State**: SQLite FTS and PostgreSQL search projections are operational indexes rebuilt directly from Markdown content.
- **Lexical Indexing Priorities**: Search relevance is weighted strictly across four semantic fields in descending order: title, normalized OKF tags, OKF description, and user-authored body content. Metadata such as paths, file extensions, and raw source references are excluded from ranking.
