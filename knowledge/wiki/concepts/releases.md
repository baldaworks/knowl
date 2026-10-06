---
type: topic
title: Release History
knowl:
  id: concepts/releases
  source_refs:
    - wiki-filesystem:knowl-docs/releases/v0.1.0.md@183d6c7f742a0555fa5f792ea5d84435ede6851d29fb5007301c8e8144bfb561
    - wiki-filesystem:knowl-docs/releases/v0.2.0.md@4d7a07f5afbdfc49ae07e300c851d7f8ab6a03bbb25b93c42203f8102c18bfe0
    - wiki-filesystem:knowl-docs/releases/v0.3.0.md@68b799fdc477f93914692c3f66603905eee292e9bb64a1362063b090f319bee1
    - wiki-filesystem:knowl-docs/releases/v0.3.1.md@b1edbf1e4b747269157429171f860cbf7f1dce300304dedc7e75b6f528c0dd30
    - wiki-filesystem:knowl-docs/releases/v0.4.0.md@d4c8e0eb36bbb174da0c8d166fadc1719e46376f3fc059bfd6d9dc651148f45c
    - wiki-filesystem:knowl-docs/releases/v0.5.0.md@a5c47b8777193be18a796d6a230a8af26040ea368d6b9ef1447610bf023f08a7
    - wiki-filesystem:knowl-docs/releases/v0.6.0.md@8d47fd9686a95f88837e18b9632061faa2fa3d6372ae3645b70ff5fda3985487
---
# Release History

This page tracks durable release milestones and historical release facts for Knowl.

## Knowl v0.1.0 — Crash-safe Knowledge Loop

Knowl v0.1.0 is the initial release establishing the self-hosted knowledge sidecar for agentic applications.

### Proven Capabilities

- **Restart Durability**: Accepted immutable sources and their execution descriptors survive host restart.
- **Idempotent Execution**: A single application runner resumes interrupted work to a terminal `completed` or `failed` state with at-least-once execution and idempotent canonical commit.
- **Canonical vs. Projected State**: Markdown documents and immutable raw sources form the canonical workspace; SQLite, PostgreSQL, and lexical search indices function as rebuildable projections.
- **Lexical Retrieval Benchmark**: Relevance-ordered lexical queries return match-centered bounded excerpts, achieving 12/12 expected top-five retrieval hits across MCP, HTTP, SQLite, and PostgreSQL on the deterministic project-decisions corpus.
- **Source-Aware Maintenance**: Updates preserve existing semantic decisions rather than creating duplicate pages.
- **Authentication**: HTTP business endpoints and MCP require a configured operator bearer token; `/healthz` and `/readyz` serve as unauthenticated public probes.
- **Public Contract**: Exposes three core operations: `knowl_retrieve`, `knowl_ingest`, and `knowl_operation` via MCP and HTTP.

### Deployment and Operations

- **Container Image**: Published to `ghcr.io/baldaworks/knowl:v0.1.0`; production deployments recommend pinning immutable image digests (`ghcr.io/baldaworks/knowl@sha256:<published-digest>`).
- **Storage Volume**: Requires persisting `/var/lib/knowl` containing the canonical workspace and SQLite operational state.
- **Upgrade & Rollback**:
  - Snapshots of persistent volumes should be taken prior to upgrades.
  - Rollbacks restore the previous known-good image digest against the retained volume.
  - Pending operations must not be discarded, volumes must not be deleted, and down migrations must not be executed; operational schema changes are additive and retained for forward recovery.

### Initial Release Boundaries

v0.1.0 intentionally excludes user interfaces, connector frameworks, vector embeddings, multi-tenancy, Git synchronization, generic memory interfaces, and section-level provenance.

## Knowl v0.2.0 — Multi-source Wiki Sync

Knowl v0.2.0 introduces a production filesystem source engine and OKF v0.2 representation for multi-source wiki synchronization in sidecar and embedded modes.

### Multi-source Synchronization Engine

- **Identity & Collision Prevention**: Multiple configured sources use `(source_id, document_id, revision)` logical tuples, preventing collisions between identical relative file paths across different sources.
- **Convergence**: Handles initial sync, unchanged documents, selective updates, deletions, interrupted scans, restarts, and source failure isolation.
- **Selective Ingestion & Tombstoning**: Unchanged documents are not re-fetched or rewritten. Deleted source files are tombstoned only after a complete scan, preserving immutable raw revisions.
- **Filtered Retrieval**: Retrieval supports optional source filters, returning source ID, document ID, revision, canonical URI, title, and bounded evidence snippets.
- **CLI Source Management**: Provides `knowl source list`, `knowl source sync <id>`, `knowl source sync --all`, and `knowl source status <id>` using the shared Host engine.
- **Provider-Free Operation**: Retrieval, linting, health probes, MCP queries, and source synchronization do not require a maintainer provider. Ingest without a provider returns `maintainer_unavailable`.
- **OKF v0.2 Bundle**: Canonical `wiki/` serves as a portable OKF v0.2 bundle; reserved control pages (`index.md`, `log.md`) are excluded from retrieval evidence. Attested computation declarations remain inert metadata.

### Bootstrap and Adoption

- **Bootstrap Commands**: `knowl bootstrap wiki <path>`, `knowl bootstrap obsidian <path>`, and `knowl bootstrap okf <path>` adopt existing workspaces into `wiki/sources/<source_id>/**` without replacing curated `wiki/index.md`.
- **Migration**: Legacy canonical workspaces (including `wiki/notes/**`) are migrated explicitly via `knowl migrate okf-v0.2`, creating an audit archive and committing markers last.

### Sidecar Operations and Limits

- **Read-Only Mounts**: Sources must be mounted read-only while `/var/lib/knowl` remains persistent. Source sync failures do not break `/readyz`.
- **Intentional Limits**: v0.2.0 remains pull-only and filesystem-focused, excluding remote wiki adapters, write-back, automated synthesis, and embeddings.

## Knowl v0.3.0 — Semantic Source Maintenance

Knowl v0.3.0 transforms configured sources into immutable evidence for one maintainer-owned semantic wiki, ending the materialization of active source mirrors in the portable OKF bundle.

### Breaking Changes

- **Required Maintainer Provider**: A runnable Host requires either an explicitly injected maintainer or a valid `knowl.provider` selected from `runtime.providers` before `/readyz` readiness.
- **Elimination of Source Mirror Pages**: Filesystem synchronization stores exact accepted revisions under `raw/` and stops materializing active `wiki/sources/<source_id>/**` mirrors.
- **Asynchronous Maintenance**: A successful source sync indicates raw acceptance and durable maintenance reservation; LLM-backed wiki updates commit asynchronously.

### Semantic Wiki Behavior

- **Semantic Taxonomy**: The maintainer synthesizes facts into shared, root-reachable `entities/`, `concepts/`, and `syntheses/` pages instead of mirroring source paths.
- **Provenance & Lineage Preservation**: Factual pages cite accepted raw source references in `knowl.source_refs`. Edits preserve unrelated source lineages and may only replace prior revisions from the same source document lineage.
- **Shared Multi-Source Evidence**: Pages supported by multiple sources return their sorted collection of contributing source documents; filtering by any contributing source returns the shared page.
- **Subtree Migration**: Legacy derived `wiki/sources/<source_id>/**` subtrees are automatically cleaned up on the source's next successful reconciliation while raw revisions and curated pages are preserved.
- **Published Artifacts**: Multi-platform Linux images for `amd64` and `arm64` published to `ghcr.io/baldaworks/knowl:v0.3.0`.

## Knowl v0.3.1 — Legacy Provenance Backfill

Knowl v0.3.1 resolves source-filtered retrieval issues on workspaces upgraded from pre-provenance revisions.

### Backfill and Provenance Fixes

- **One-Way Conflict-Safe Enrichment**: Source reconciliation backfills legacy raw manifests with validated `source_document` identity while reusing already accepted immutable bytes without re-fetching from connectors. Existing provenance cannot be overwritten.
- **Projection Rebuild**: Synchronizing sources backfills page-to-source relations and `source_documents` projections for semantic pages citing legacy revisions, even if maintenance operations are already terminal. As a result, `knowl retrieve --source <id>` returns the same relevant semantic evidence as unfiltered retrieval.
- **Published Artifacts**: Linux container images for `amd64` and `arm64` published to `ghcr.io/baldaworks/knowl:v0.3.1`.

## Knowl v0.4.0 — Connected Sources and Publishable Wikis

Knowl v0.4.0 expands source-to-publication capabilities by adding remote Git support, durable one-shot processing, explicit hierarchy planning, and portable bundle exports.

### Remote Git Sources

- **Git Synchronization**: Remote Git sources join filesystem sources under the unified synchronization, status, retry, and maintenance lifecycle.
- **Authentication & Limits**: HTTPS and SSH credentials remain external; SSH host keys, repository identity, cache size (default 512 MiB), and transfer caps (default 500 MiB) fail closed.
- **Integrity**: Branch rewrites, moved tags, and repository rebinding require explicit operator acknowledgement.

### Durable One-Shot Operations & Recovery

- **One-Shot CLI**: `knowl run` executes bounded source synchronization, drains queued maintenance operations, and optionally reconciles hierarchy without running the HTTP daemon.
- **Failure Recovery**: Maintenance failures record stable failure classes (`provider`, `source`, `staging`, `maintenance_policy`) and bounded retry state; operators can inspect, dry-run preview, and requeue failures via `knowl source retry`.

### Semantic Hierarchy and Publication

- **Explicit Hierarchy**: `knowl hierarchy reconcile` deterministically organizes ordinary pages into subject-first, root-reachable OKF catalog structures under `hierarchy-v3`.
- **Bundle Export**: `knowl export okf` copies the canonical portable wiki bundle without raw sources, operational state, or SQLite databases.
- **LLM Navigation**: `knowl export llms-txt` renders bundle-relative navigation to stdout or file with optional metadata.

### Retrieval, Limits, and Storage

- **Tag Indexing & Relaxed Matching**: Lexical search indexes normalized OKF tags and provides relaxed fallback matching when strict queries yield no results.
- **Document Ceilings**: Default per-document maintenance ceiling increased to 262,144 characters (with independent 4 MiB byte guard).
- **Database Migrations**: Additive operational migrations support hierarchy operations, tag projections, retry state, and Git repository identity. Rolling back after the first hierarchy run requires restoring the pre-upgrade operational database.
- **Published Artifacts**: Linux images for `amd64` and `arm64` published to `ghcr.io/baldaworks/knowl:v0.4.0`.

## Knowl v0.5.0 — Policy-Aware Maintenance and Isolated Validation

Knowl v0.5.0 introduces policy-aware maintenance cycles, open-world link handling, and failure isolation for generated documents.

### Policy-Aware Maintenance

- **Policy Generation in Identity**: Deterministic, non-secret policy generation participates in durable maintenance identity. Changes to output-affecting rules (such as read ceilings) make unchanged source revisions eligible for one new bounded maintenance cycle.
- **Ceiling Reconsideration**: Documents that previously failed under the former 32,768-character ceiling can be reconsidered under the 262,144-character default without source modifications. Repeated scans under the same generation converge without duplicate work.
- **Safe Transitions**: Automatic reconsideration is restricted to safe source-policy transitions; staging, provider, and unknown failures retain manual retry gates.

### Open-World Links and Provenance Isolation

- **Open-World Links**: Unresolved original links proven to exist in accepted Markdown are preserved and reported via bounded, redacted diagnostics rather than invalidating staging. Invented links, malformed paths, and escaping targets remain rejected.
- **Document Failure Isolation**: Invalid Knowl-owned provenance (e.g. `citation.unknown_source`) is isolated to the affected document; valid siblings can commit and reach the index while the prior canonical version of the rejected document remains readable.
- **Withheld Broken Catalogs**: Catalog edits that would publish a broken graph are withheld with the smallest deterministic dependent closure.

### Durable Recovery and Git Source Hardening

- **Operational Store Persistence**: SQLite and PostgreSQL persist maintenance generation and bounded diagnostics with equivalent retry, restart, and idempotent replay behavior, including an explicit compatibility path for legacy pre-diagnostics state.
- **Git Cache Hardening**: Git cache lineage and repository identity checks reject stale, rebound, or structurally incomplete cache state while preserving bounded recovery.
- **Operational Migrations**: Operational-store migrations support maintenance generation and diagnostics. Rolling back across migrations requires restoring the pre-upgrade operational database.
- **Published Artifacts**: Multi-platform Linux images for `amd64` and `arm64` published to `ghcr.io/baldaworks/knowl:v0.5.0`.

## Knowl v0.6.0 — Local Codex Plugin and npm Staging

Knowl v0.6.0 introduces the repository-supplied local Codex plugin and stages the CLI launcher for npm as `@baldaworks/knowl@0.6.0`.

### Local Codex Plugin and npm Distribution

- **Bootstrap**: Local project root initialization via `npx --yes @baldaworks/knowl@0.6.0 setup codex`.
- **Plugin Architecture**: Provides `setup` and `run` skills and registers the three Knowl core tools via host-owned MCP stdio without requiring a background daemon, open port, operator token, or manual `codex mcp add`.
- **Version Pinning**: The `run` skill accepts a local CLI only when its structured version identity matches exact `0.6.0`; otherwise execution falls back to the pinned `@baldaworks/knowl@0.6.0` npm launcher.
- **Native Package Coverage**: Stages five scoped native packages under the MIT license covering macOS (x64/arm64), Linux (x64/arm64), and Windows (x64).
- **Marketplace & Offline Prewarming**: Setup supports npm artifacts and a pinned sparse Git marketplace. Offline usage requires prewarming exact artifacts while network access is available.
- **Verified Scope & Concurrency**: Supported exclusively for local Codex projects with local filesystem storage. Concurrent processes for a single project coordinate durable storage and workspace while separate working directories remain isolated.
- **Publication Requirements**: Publication requires an authorized release workflow with a v0.6.0 tag on `main`, green verification checks (repository, integration, container, npm), and publish authority credentials for `@baldaworks/knowl` and native packages.

See [[concepts/architecture]] for architectural details, [[concepts/public-contract]] for service APIs, [[concepts/service-operations]] for operational procedures, and [[concepts/content-and-trust-boundaries]] for trust models.
