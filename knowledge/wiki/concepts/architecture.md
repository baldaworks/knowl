---
type: topic
title: Product Architecture
knowl:
  id: concepts/architecture
  source_refs:
    - wiki-filesystem:knowl-docs/design.md@3a68ce3bd879e93c51035a4f8f0cf5c198e5019718caf580e388c947b63f1749
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
- `internal/source`: Filesystem and remote Git adapters, normalization, and reconciliation.
- `pkg/knowl`: Composition root and host API; the sole package that composes multiple adapters.
- `pkg/knowlfx`, `cmd/knowl`: Fx and CLI entrypoints.

Supported Go import paths are `pkg/knowl`, `pkg/knowlfx`, `pkg/knowl/mcp`, and `pkg/knowl/types`.

## Synchronization and Migration

- **Bootstrap**: An optional CLI preflight that initializes workspace and configuration, verifies path separation, and executes an initial sync.
- **Ordinary Sync**: Persists immutable raw evidence, reserves idempotent maintenance work, and invokes the maintainer to update semantic pages. The operation scheduler uses one worker by default, or two isolated maintainer owners when configured. Upstream source deletions tombstone active source state and remove legacy `wiki/sources/<source_id>/**` subtrees during subsequent sync while preserving raw revisions and curated pages.
- **Git Sources**: Read-only synchronization fetches the configured HTTPS or SSH branch/tag and pins a complete scan to one immutable commit. Only regular blobs are consumed; repository content is never executed and remotes are never modified. Git sources share the synchronization, scheduling, status, retry, and maintenance lifecycle with filesystem sources.
- **Migration**: Format upgrades are performed explicitly via `knowl migrate okf-v0.2`, preserving legacy logs in an archive.

## Maintenance Execution Ownership

- **Execution Owners**: `knowl.workers` configures one or two fixed execution owners. Each owner maintains an independent RuntimeMaintainer, agent, session service, and source/hierarchy services while sharing policy identity and canonical storage.
- **Dispatcher and Leases**: A finite dispatcher drives bounded worker loops. Admission precedes durable claims, and active claims renew leases. Background workers, joined Drain invocations, direct hierarchy, and optional model-lint share this same owner pool.
- **Host Publication Gate**: Inference and output correction hold only their execution owner. A single host publication gate serializes canonical commit, snapshot projection, and outcome (including source-sync saga recovery and hierarchy no-op finalization).
- **Typed Conflict Handling**: Preimage mismatches for stale log, catalog, or page files fail permanently with typed conflicts without automatic replanning.

## Invariants and Non-Goals

- Provenance is durable and inspectable; local defaults are bounded and deterministic.
- Startup validates workspace integrity, storage initialization, recovery, and projections before readiness.
- Canonical writes strictly preserve single-writer ordering.
- Non-goals include session memory, user-fact memory, agent orchestration, final-answer generation, vector database canonical storage, web crawling, image/binary comprehension.
- Configured remote Git sources are synchronized into the workspace. Operators own version control and publication of the generated workspace; Knowl does not commit or push it to Git.

See [[concepts/public-contract]] for interface endpoints and [[concepts/content-and-trust-boundaries]] for storage and trust models.
