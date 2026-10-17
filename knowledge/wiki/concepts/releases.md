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
    - wiki-filesystem:knowl-docs/releases/v0.6.1.md@d53662be06fe5ef4a9ce2f9380451d91851ae1537f12455b6445dc64e13e0c68
    - wiki-filesystem:knowl-docs/releases/v0.7.0.md@506ab3b58c1915b4712ddd18a0c41fcffe4c85f123ee144e4b0148a5f40cec3b
    - wiki-filesystem:knowl-docs/releases/v0.7.1.md@212713471924fdd179dfd137bfa1a6bd6651e4ef9c24cec9e051ec96d313b23f
---
# Release History

This page tracks durable release milestones and historical release facts for Knowl.

## Knowl v0.1.0 — Crash-safe Knowledge Loop

Knowl v0.1.0 is the initial release establishing the self-hosted knowledge sidecar for agentic applications.

### Proven Capabilities

- **Restart Durability**: Accepted immutable sources and their execution descriptors survive host restart.
- **Idempotent Execution**: A single application runner resumes interrupted work to a terminal `completed` or `failed` state with at-least-once execution and idempotent canonical commit.
- **Canonical vs. Projected State**: Markdown documents and immutable raw sources form the canonical workspace; SQLite, PostgreSQL, and lexical search indices function as rebuildable projections.
- **Lexical Retrieval Benchmark**: Relevance-ordered lexical queries return match-centered bounded excerpts, achieving 12/12 expected top-five retrieval hits across MCP, HTTP, SQLite, and PostgreSQL on the deterministic project-decisions corpus with source references.
- **Source-Aware Maintenance**: Updates preserve existing semantic decisions rather than creating duplicate pages.
- **Authentication**: HTTP business endpoints and MCP require a configured operator bearer token; `/healthz` and `/readyz` serve as unauthenticated public probes.
- **Public Contract**: Exposes three core operations: `knowl_retrieve`, `knowl_ingest`, and `knowl_operation` via MCP and HTTP.

### Deployment and Operations

The historical v0.1.0 image recipe is:

```bash
docker pull ghcr.io/baldaworks/knowl:v0.1.0
docker run --rm \
  -p 127.0.0.1:8080:8080 \
  -e KNOWL_OPERATOR_TOKEN=replace-with-a-strong-secret \
  -v knowl-data:/var/lib/knowl \
  ghcr.io/baldaworks/knowl:v0.1.0
```

The release workflow appends the immutable manifest digest to the GitHub Release. Production deployments replace the mutable tag with `ghcr.io/baldaworks/knowl@sha256:<published-digest>`.

Persist `/var/lib/knowl`, which contains the canonical raw/Markdown workspace and SQLite operational state. In v0.1.0, `GET /readyz` succeeds only after workspace recovery, durable-work resumption setup, and projection preparation.

The historical upgrade procedure is:

1. Back up or snapshot the persistent volume according to local policy.
2. Stop the old container without deleting `/var/lib/knowl`.
3. Start v0.1.0 by immutable image digest with the same volume and operator token.
4. Wait for `/readyz`, then verify authenticated retrieve and operation reads.

For rollback, stop the new container and run the previous known-good image digest against the retained volume. Do not discard pending operations, delete the volume, or run destructive down migrations. Additive operational schema is retained for forward recovery.

### Initial Release Boundaries

v0.1.0 intentionally excludes user interfaces, connector frameworks, vector embeddings, multi-tenancy, Git synchronization, generic memory interfaces, and section-level provenance. Hosts decide which events are durable and generate final user answers; Knowl maintains inspectable project knowledge and returns bounded provenance-backed evidence.

## Knowl v0.2.0 — Multi-source Wiki Sync

Knowl v0.2.0 introduces a production, provider-free filesystem source engine and OKF v0.2 representation for durable read-only wiki mirrors in sidecar and embedded modes. These source mirrors describe historical behavior, superseded by semantic source maintenance in v0.3.0.

### Multi-source Synchronization Engine

- **Identity & Collision Prevention**: Multiple configured sources use `(source_id, document_id, revision)` logical tuples, preventing collisions between identical relative file paths across different sources.
- **Convergence**: Initial sync, unchanged documents, selective updates, complete deletion, interrupted scans, restarts, and source failure isolation converge through one production Host.
- **Selective Ingestion & Tombstoning**: Unchanged documents are not re-fetched or rewritten. Deleted active mirrors are tombstoned only after a complete scan, preserving immutable raw revisions.
- **Filtered Retrieval**: Retrieval supports optional source filters, returning source ID, document ID, revision, canonical URI, title, and bounded evidence snippets.
- **CLI Source Management**: Provides `knowl source list`, `knowl source sync <id>`, `knowl source sync --all`, and `knowl source status <id>` using the same engine as embedded Host calls.
- **Provider-Free Operation**: Retrieval, linting, health probes, MCP reads, and source workflows do not require a maintainer provider. Provider-free ingest remains registered and returns the stable `maintainer_unavailable` outcome.
- **Public Contract**: MCP retains exactly `knowl_retrieve`, `knowl_ingest`, and `knowl_operation`; no source-management tool is added.
- **OKF v0.2 Bundle**: Canonical `wiki/` serves as a directly portable OKF v0.2 bundle. Structured metadata is preserved through filesystem, SQLite/PostgreSQL, CLI, HTTP, and MCP surfaces. Reserved controls never become retrieval evidence, and Attested Computation declarations remain inert metadata that is never executed.

### Bootstrap and Adoption

`knowl bootstrap wiki <path>`, `knowl bootstrap obsidian <path>`, and `knowl bootstrap okf <path>` require fresh workspaces. They construct deterministic filesystem sources named `bootstrap-wiki`, `bootstrap-obsidian`, or `bootstrap-okf` and perform one shared-engine sync. Mirrors live under `wiki/sources/<source_id>/**`; bootstrap preserves curated `wiki/index.md`.

In v0.2.0, newly generated bootstrap configuration is provider-free and retains its source. Existing operator-owned configuration is preserved unchanged; ongoing synchronization requires explicitly adding the corresponding source entry.

Legacy workspaces, including `wiki/notes/**`, are never silently migrated. After backup, `knowl migrate okf-v0.2` performs journaled, idempotent migration, preserves legacy content, archives the exact prior log, commits its marker last, and rebuilds projections. Operators then configure named sources, verify mirrors and retrieval evidence, and curate or retire legacy pages according to local policy.

### Sidecar Operations

Mount every source root read-only and persist `/var/lib/knowl`. The historical sidecar example supplies `engineering` and `operations` fixtures, on-start sync, five-minute intervals, and capped retry without a provider. Source failures leave readiness unchanged; durable source status identifies the failing root while retrieval continues serving the last successful snapshot.

### Upgrade and Rollback

The historical v0.2.0 upgrade procedure is:

1. Back up the persistent workspace and operational store.
2. Configure unique filesystem source IDs and read-only roots.
3. Start v0.2.0 and wait for `/readyz`.
4. Run `knowl source list`, `knowl source sync --all`, and per-source status checks.
5. Verify both unfiltered and source-filtered retrieval before changing legacy pages.

Rollback stops the new process and restores the prior image and configuration together. Preserve `raw/`, `.knowl/`, and source mirrors: new raw revisions and source state are non-destructive evidence, though older binaries may ignore additive state. Never run destructive down migrations.

### Intentional Limits and Validation

v0.2.0 is pull-only and filesystem-first. It adds no Git or remote-wiki adapters, write-back, export/publish, cross-source deduplication, automatic synthesis, embeddings, UI, or source-management HTTP/MCP operations.

Its rollout gate runs full Go tests, focused race tests, strict golangci-lint, module verification, integration-tagged PostgreSQL source tests, sidecar/config parsing, and source-layer/packaging audits.

## Knowl v0.3.0 — Semantic Source Maintenance

Knowl v0.3.0 transforms configured sources into immutable evidence for one maintainer-owned semantic wiki. Source revisions no longer become pages inside the portable OKF bundle.

### Breaking Changes

- **Required Maintainer Provider**: A runnable Host requires either an explicitly injected maintainer or a valid `knowl.provider` selected from `runtime.providers` before `/readyz` readiness.
- **Elimination of Source Mirror Pages**: Filesystem synchronization stores exact accepted revisions under `raw/` and stops materializing active `wiki/sources/<source_id>/**` mirrors.
- **Asynchronous Maintenance**: A successful source sync indicates raw acceptance and durable maintenance reservation. Model-backed wiki updates complete asynchronously and are reported separately by source status.

Bootstrap remains optional. `sync.on_start` remains explicit per-source configuration and can be disabled in favor of manual synchronization.

### Semantic Wiki Behavior

- **Semantic Taxonomy**: The maintainer creates and updates root-reachable semantic OKF entities, concepts, syntheses, and catalogs instead of copying source paths.
- **Provenance & Lineage Preservation**: Factual pages cite accepted raw source references in `knowl.source_refs`. Edits preserve unrelated source lineages and may only replace older revisions from the same source document lineage.
- **Shared Multi-Source Evidence**: Related evidence from multiple configured sources can support one page. Retrieval returns its sorted `source_documents` collection; filtering by any contributing source returns the shared page.
- **Retrieval Controls and Projections**: Catalog and source-mirror control pages are excluded from retrieval evidence. SQLite and PostgreSQL use the same rebuildable page-to-source projection.

### Upgrade and Source Reconciliation

The historical v0.3.0 upgrade procedure is:

1. Stop active writers and back up the complete workspace and operational store.
2. Configure `runtime.providers` and select a provider with `knowl.provider` before starting the new image.
3. Deploy `ghcr.io/baldaworks/knowl:v0.3.0` and wait for `/readyz`.
4. Run `knowl source sync --all`, then inspect each `knowl source status`.
5. Wait for queued maintenance operations to commit and verify unfiltered and source-filtered retrieval.

On each source's next successful reconciliation, Knowl removes only that source's legacy derived `wiki/sources/<source_id>/**` subtree through staged recovery. Existing raw revisions and curated semantic pages remain unchanged. Upstream deletion tombstones source state without implicitly deleting accumulated knowledge.

Source status exposes deterministic bounded queued, replayed, committed, and failed maintenance counts and samples. Structured logs correlate source, document, revision, and operation identifiers without source bodies, prompts, credentials, or provider secrets.

### Published Artifact and Validation

The release workflow publishes and attests `ghcr.io/baldaworks/knowl:v0.3.0` as a multi-platform Linux image for `amd64` and `arm64`. Production deployments pin the digest emitted by the GitHub Release.

Before publication, the release gate verifies modules and generated OpenAPI bindings, runs the full race-enabled Go suite, vulnerability and architecture checks, strict linting, the PostgreSQL integration contract, and a release-shaped persistent-volume sidecar smoke test.

## Knowl v0.3.1 — Legacy Provenance Backfill

Knowl v0.3.1 fixes source-filtered retrieval after upgrading workspaces whose raw revisions were accepted before configured-source provenance was stored in raw manifests.

### Backfill and Provenance Fixes

- **One-Way Conflict-Safe Enrichment**: Source reconciliation enriches legacy raw manifests with validated `source_document` identity while reusing already accepted immutable bytes. Unchanged documents are not fetched from the connector again. Existing provenance cannot be replaced by a different document identity.
- **Projection Rebuild**: The same source sync records page-to-source relations and `source_documents` projections for semantic pages citing legacy revisions, even when all maintenance operations are already terminal. Consequently, `knowl retrieve --source <id>` returns the same relevant semantic evidence as unfiltered retrieval.

Bootstrap and `sync.on_start` remain optional. Source documents remain outside the portable semantic wiki, and legacy `wiki/sources/<source_id>/**` mirrors remain excluded.

### Upgrade and Published Artifact

The historical v0.3.1 upgrade procedure is:

1. Stop active writers and back up the complete workspace and operational store.
2. Deploy `ghcr.io/baldaworks/knowl:v0.3.1`.
3. Run `knowl source sync --all` once to backfill legacy provenance and rebuild the projection, including when maintenance operations are already terminal.
4. Verify both unfiltered and `--source` retrieval for an existing semantic page.

The release workflow publishes and attests `ghcr.io/baldaworks/knowl:v0.3.1` as a multi-platform Linux image for `amd64` and `arm64`. Production deployments pin the digest emitted by the GitHub Release.

## Knowl v0.4.0 — Connected Sources and Publishable Wikis

Knowl v0.4.0 expands the complete source-to-publication workflow with remote Git sources, durable maintenance recovery and retry, explicit semantic hierarchy, and portable OKF export with `llms.txt` navigation.

### Remote Git Sources

- **Git Synchronization**: Remote Git sources join filesystem sources under the same bounded synchronization, status, retry, and maintenance lifecycle.
- **Authentication & Limits**: HTTPS and SSH authentication remain external to configuration. SSH host keys, immutable repository identity, ref movement, cache size (default 512 MiB), and pack transfer limits (default 500 MiB) are validated fail-closed.
- **Integrity**: Branch rewrites, moved tags, and repository rebinding require explicit operator acknowledgement. Logs and errors redact credentials and content. Git synchronization closes acceptance gaps around rewritten refs, cache safety, bounded traversal, and provenance continuity.

### Durable One-Shot Operations & Recovery

- **One-Shot CLI**: `knowl run` executes bounded source synchronization, drains queued maintenance operations, and optionally reconciles hierarchy without keeping the HTTP service running.
- **Failure Recovery**: Maintenance failures record stable failure classes (`provider`, `source`, `staging`, `maintenance_policy`) and bounded retry state; operators can inspect, preview, and requeue selected failures via `knowl source retry`.
- **Interruption Recovery**: Durable work resumes after interruption while preserving the last successful source checkpoint and searchable wiki projection.

### Semantic Hierarchy and Publication

- **Explicit Hierarchy**: `knowl hierarchy reconcile` reorganizes ordinary pages into a subject-first, root-reachable OKF catalog graph under `hierarchy-v3`. Planning is deterministic, bounded, staged, and conflict-checked. Source synchronization never invokes hierarchy implicitly.
- **Bundle Export**: `knowl export okf` copies the canonical portable wiki without raw evidence, operational state, configuration, or databases.
- **LLM Navigation**: `knowl export llms-txt` renders bundle-relative navigation to stdout or a selected file, with optional title, summary, and public base URL.

### Retrieval, Limits, and Storage

- **Tag Indexing & Relaxed Matching**: Search indexes semantic OKF tags and falls back to native relaxed matching when a strict query has no results.
- **Document Ceilings**: The default wiki document ceiling increased from 32,768 to 262,144 characters. Documents exactly at the limit are accepted; larger documents fail before provider invocation, alongside an independent 4 MiB byte guard.
- **Database Migrations**: Additive operational-store migrations support generic hierarchy operations, semantic tag projection, retry state, and Git repository identity while preserving existing source operations and canonical Markdown.
- **Ownership Compatibility**: The HTTP, MCP, and embedded Go ownership model remains unchanged.

### Compatibility and Upgrade

The historical v0.4.0 upgrade procedure is:

1. Stop active writers and back up the workspace and operational store.
2. Deploy `ghcr.io/baldaworks/knowl:v0.4.0` and wait for `/readyz`.
3. Review new Git source configuration, especially credentials, `known_hosts`, repository identity, and rewrite policy.
4. Run `knowl run` or the existing source synchronization workflow, then inspect `knowl source status` until maintenance is terminal.
5. Optionally run `knowl hierarchy reconcile`, validate the workspace, and generate publication output with `knowl export okf` and `knowl export llms-txt`.

After the first hierarchy operation, binary rollback also requires restoring the pre-upgrade operational database. Canonical Markdown remains portable and can rebuild a fresh projection.

### Published Artifact and Validation

The release workflow publishes and attests `ghcr.io/baldaworks/knowl:v0.4.0` as a multi-platform Linux image for `amd64` and `arm64`. Production deployments pin the immutable digest emitted by the GitHub Release.

Before publication, the release gate verifies modules and generated OpenAPI bindings, runs the full race-enabled Go suite, vulnerability and architecture checks, strict linting, the PostgreSQL integration contract, and a release-shaped persistent-volume sidecar smoke test.

## Knowl v0.5.0 — Policy-Aware Maintenance and Isolated Validation

Knowl v0.5.0 introduces policy-aware maintenance cycles, open-world link handling, and failure isolation for generated documents.

### Policy-Aware Maintenance

- **Policy Generation in Identity**: Deterministic, non-secret policy generation participates in durable maintenance identity. Changes to output-affecting rules (such as effective read ceilings) make unchanged source revisions eligible for one new bounded maintenance cycle.
- **Ceiling Reconsideration**: Documents that previously failed under the former 32,768-character ceiling can be reconsidered under the 262,144-character default without source modifications. Repeated scans under the same generation converge without duplicate work.
- **Safe Transitions**: Automatic reconsideration is restricted to safe source-policy transitions; staging, provider, and unknown failures retain explicit manual retry gates.

### Open-World Links and Provenance Isolation

- **Open-World Links**: Unresolved original links proven to exist in accepted Markdown are preserved and reported via bounded, redacted diagnostics rather than invalidating staging. Invented unresolved links, malformed paths, and escaping targets remain rejected.
- **Document Failure Isolation**: Invalid Knowl-owned provenance (e.g. `citation.unknown_source`) is isolated to the affected document; valid siblings can commit and reach the index while the prior canonical version of the rejected document remains readable.
- **Withheld Broken Catalogs**: Catalog edits that would publish a broken graph are withheld with the smallest deterministic dependent closure.

### Durable Recovery and Git Source Hardening

- **Operational Store Persistence**: SQLite and PostgreSQL persist maintenance generation and bounded diagnostics with equivalent retry, restart, and idempotent replay behavior.
- **Legacy Compatibility**: Legacy rows use an explicit compatibility path, including safe replay of empty pre-diagnostics state, without weakening malformed-state checks.
- **Git Cache Hardening**: Git cache lineage and repository identity checks reject stale, rebound, or structurally incomplete cache state while preserving bounded recovery.

### Compatibility and Upgrade

The HTTP, MCP, provider, source configuration, and canonical Markdown contracts remain compatible. Operational-store migrations add maintenance generation and diagnostics while preserving existing operations and source history.

The historical v0.5.0 upgrade procedure is:

1. Stop active writers and back up the workspace and operational store.
2. Deploy `ghcr.io/baldaworks/knowl:v0.5.0` and wait for `/readyz`.
3. Run `knowl run` or the configured synchronization workflow.
4. Inspect `knowl source status` for policy-triggered work, unresolved original links, isolated document rejections, and failures requiring manual class-filtered retry.

Rolling back across the new operational migrations requires restoring the pre-upgrade operational database. Canonical Markdown and accepted raw source history remain unchanged and portable.

### Published Artifact and Validation

The release workflow publishes and attests `ghcr.io/baldaworks/knowl:v0.5.0` as a multi-platform Linux image for `amd64` and `arm64`. Production deployments pin the immutable digest emitted by the GitHub Release.

Before publication, the release gate verifies modules and generated OpenAPI bindings, runs the full race-enabled Go suite, vulnerability and architecture checks, strict linting, the PostgreSQL integration contract, and a release-shaped persistent-volume sidecar smoke test.

## Knowl v0.6.0 — Local Codex Plugin and npm Staging

Knowl v0.6.0 introduces the repository-supplied local Codex plugin and stages the CLI launcher for npm as `@baldaworks/knowl@0.6.0`. The launcher depends on five scoped native packages; all packages declare MIT license metadata. The release note records implementation and verification in the repository without claiming npm publication.

### Local Codex Plugin and npm Distribution

- **Bootstrap**: The historical local-project bootstrap command is `npx --yes @baldaworks/knowl@0.6.0 setup codex`.
- **Plugin Architecture**: Contains only the `setup` and `run` skills and registers the same three Knowl tools through host-owned MCP stdio. It needs no local HTTP daemon, port, operator token, or separate `codex mcp add`. Existing explicit HTTP sidecar and embedded-Go deployments remain supported.
- **Version Pinning**: The `run` skill accepts a local CLI only when its structured version identity is exactly `0.6.0`; otherwise the complete run stays on the pinned `@baldaworks/knowl@0.6.0` npm launcher. Project maintenance still requires the configured maintainer provider.
- **Native Package Coverage**: Staged packages cover macOS x64/arm64, Linux x64/arm64, and Windows x64.
- **Marketplace & Offline Prewarming**: Setup can fetch npm artifacts and a pinned sparse Git marketplace. Sparse checkout limits materialized paths without guaranteeing a metadata-only fetch. Offline use requires prewarming those exact artifacts while reachable.
- **Verified Scope & Concurrency**: Verified for local Codex with project state on a local filesystem. Hosted or remote plugin execution, remote MCP hosting, and shared/network filesystems are unsupported by this release. Concurrent local processes for one project coordinate the durable store and workspace; separate project working directories remain isolated.
- **Publication Requirements**: Publication requires a separately authorized release workflow, a v0.6.0 tag on `main`, green repository/integration/container/npm checks, and npm credentials with publish authority for `@baldaworks/knowl` and its native packages.

## Knowl v0.6.1 — npm Trusted Publishing and Plugin Release

Knowl v0.6.1 prepares the local Codex plugin and five native CLI packages for publication through npm trusted publishing. It accumulates changes since v0.5.0 after the v0.6.0 publication stopped before uploading artifacts because its workflow required a token.

### Changes and Publication

- **npm Trusted Publishing**: Uses GitHub Actions OIDC via Omnidist without a stored publication token. Each npm package must first have its trusted publisher registered for `baldaworks/knowl` on workflow `release.yml`.
- **Plugin and Native Packages**: The local Codex plugin supplies `setup` and `run` skills and host-owned MCP stdio integration, with plugin, launcher, and native packages using version `0.6.1`.
- **Operator Web UI**: Optional UI provides knowledge browsing, search, processing history, source status, and saved-source inspection.
- **Hybrid Search**: Covers complete pages, preserves structured Markdown and OKF section context, and reuses authenticated embeddings when rebuilding SQLite or PostgreSQL projections.
- **Navigation and Maintained Wiki**: Wiki navigation uses portable relative Markdown links, validates navigation, exports preflight OKF documents, and excludes literal code and image descriptions from graph extraction. The repository wiki was regenerated with the configured maintainer and independently reviewed while preserving existing raw revisions and log history.

### Installation and Operations

- **Bootstrap**: Run `npx --yes @baldaworks/knowl@0.6.1 setup codex` from a local project root after publication.
- **Platform Coverage**: Native packages cover macOS (x64/arm64), Linux (x64/arm64), and Windows (x64).
- **Environment Scope**: Supports local Codex projects on local filesystems with a configured maintainer provider; existing explicit HTTP sidecar and embedded Go deployments remain supported.

### Verification Gate

Publication is gated on module and OpenAPI checks, full race test suite, vulnerability scanning, both linters, PostgreSQL integration, persistent volume container smoke tests, and verified native npm artifacts. The release workflow publishes the multi-platform container and records its immutable digest.

## Knowl v0.7.0 — Gradual Source Retirement and Azure SSH Synchronization

Knowl v0.7.0 adds gradual reconciliation for removed or replaced configured sources, preserves content rejection diagnostics, and fixes Azure SSH Git wiki synchronization.

### Source Lifecycle and Content Reconciliation

- **Durable Retirement**: Configured source owners transition through `active`, `retiring`, and `deleted`. Removing an owner schedules bounded, durable page reconciliation. Pages supported only by that owner are removed; mixed-source pages are recomputed from remaining active evidence. Immutable raw history remains available. Retirement completes only after an authoritative check finds no remaining page references.
- **Independent Replacement Owners**: Replacing a filesystem source with Git creates an independent owner rather than a citation alias. Accept the replacement before removing the old source. Reconciliation is gradual and does not provide an atomic global cutover.
- **Rejection Diagnostics**: Rejected content plans retain page paths, rule codes, and dependency reasons, including when no safe changes remain (`plan.empty_safe_subset`). Invalid content is not published.
- **Git Synchronization**: Azure SSH wiki repositories are supported, and fetched commits resolve correctly.
- **Maintained Documentation**: The repository wiki includes updated local Codex guidance and nested navigation catalogs while preserving accepted revisions and update history.

### Installation and Upgrade

After publication, the historical local-project installation command is:

```bash
npx --yes @baldaworks/knowl@0.7.0 setup codex
```

The plugin, MCP launcher, and five native npm packages use version `0.7.0`. Existing local Codex, HTTP service, and embedded Go workflows remain supported.

Before upgrading, back up the complete workspace and durable store and stop existing writers. Source retirement adds durable lifecycle and reconciliation state that requires a matching binary. Once retirement begins, prefer a forward fix over an in-place downgrade because older code does not enforce retiring-source publication authority. See [Service Operations](service-operations.md#source-retirement-status-and-recovery) for status and retry commands.

## Knowl v0.7.1 — Browser Test Harness Stabilization

Knowl v0.7.1 stabilizes the browser test harness used to verify the Web UI. Application behavior and the durable storage schema remain unchanged from v0.7.0.

### Harness Fix and Release Alignment

- **Chromium Interception Recovery**: A pass-through Playwright route remains active while one-shot fault routes expire. This prevents an interception shutdown race from stranding the UI's immediate recovery request after `snapshot_changed`.
- **Verification Preserved**: Fault injection, UI assertions, security checks, and existing deadlines remain in place. The fix adds no retries or timeout increases and changes no production UI code.
- **Version Alignment**: The Codex plugin, MCP launcher, installation guidance, and native npm package release contract align at `0.7.1`.

### Installation and Upgrade

After publication, run from a local project root:

```bash
npx --yes @baldaworks/knowl@0.7.1 setup codex
```

Native npm packages cover macOS x64/arm64, Linux x64/arm64, and Windows x64. The container remains available for Linux `amd64` and `arm64`.

This patch introduces no additional storage migration or configuration change. Upgrades from versions older than v0.7.0 follow the [v0.7.0 installation and upgrade guidance](releases.md#installation-and-upgrade), including its source retirement and downgrade cautions.

See [concepts/architecture](architecture.md) for architectural details, [concepts/public-contract](public-contract.md) for service APIs, [concepts/service-operations](service-operations.md) for operational procedures, and [concepts/content-and-trust-boundaries](content-and-trust-boundaries.md) for trust models.
