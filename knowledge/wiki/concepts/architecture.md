---
type: topic
title: Product Architecture
knowl:
  id: concepts/architecture
  source_refs:
    - wiki-filesystem:knowl-docs/design.md@15d3f23357e10afc7a1f9e7ba3dda84f262521d5ae864d5fbc4ed95c822ec4e0
---
# Product Architecture

Knowl is a self-hosted knowledge sidecar for agentic applications. It turns durable sources into an inspectable Markdown knowledge base and returns bounded, provenance-backed evidence.

## Responsibility Boundaries

- **Host Responsibilities**: Decides which events are durable, assigns immutable source revisions, orchestrates tools, and generates final user answers.
- **Knowl Responsibilities**: Owns durable acceptance, canonical raw and Markdown artifacts, operation recovery, and bounded evidence with source references.
- **Maintainer Provider**: A configured runtime provider proposes validated Markdown updates inside Knowl. It is neither an external connector nor a public interface.

## Dependency and Package Layout

Dependencies flow inward:
```text
entrypoints and surfaces -> composition -> adapters -> app policy -> shared types
```

Ownership follows this inward flow:
- `pkg/knowl/types`: Shared IDs and transport-neutral data shapes without dependencies on other Knowl packages.
- `pkg/knowl/wiki`: Wiki and frontmatter semantics.
- `pkg/knowl/app`: Business policy and consuming ports; adapters depend on it.
- `content/fs`, `store/*`, `provider`: Adapters for workspace, operational state, and provider.
- `pkg/knowl/mcp`: MCP adapter.
- `internal/httpapi`: Deterministic HTTP/OpenAPI adapter.
- `internal/mcphttp`: Streamable HTTP transport for MCP.
- `internal/source`: Filesystem adapter, normalization, and reconciliation.
- `pkg/knowl`: Composition root and host API; the sole package that composes multiple adapters.
- `pkg/knowlfx`, `cmd/knowl`: Fx and CLI entrypoints.

Supported Go import paths are `pkg/knowl`, `pkg/knowlfx`, `pkg/knowl/mcp`, and `pkg/knowl/types`.

## Synchronization and Migration

- **Bootstrap**: An optional CLI preflight that initializes workspace and configuration, verifies path separation, and executes an initial sync.
- **Ordinary Sync**: Persists immutable raw evidence, reserves idempotent maintenance work, and invokes the maintainer to update semantic pages. Upstream source deletions tombstone active source state and remove legacy `wiki/sources/<source_id>/**` subtrees during subsequent sync while preserving raw revisions and curated pages.
- **Migration**: Format upgrades are performed explicitly via `knowl migrate okf-v0.2`, preserving legacy logs in an archive.

## Invariants and Non-Goals

- Provenance is durable and inspectable; local defaults are bounded and deterministic.
- Startup validates workspace integrity, storage initialization, recovery, and projections before readiness.
- Canonical writes strictly preserve single-writer ordering.
- Non-goals include session memory, user-fact memory, agent orchestration, final-answer generation, vector database canonical storage, web crawling, image/binary comprehension, and Git push/sync.

See [[concepts/public-contract]] for interface endpoints and [[concepts/content-and-trust-boundaries]] for storage and trust models.
