---
type: concept
title: Release Lifecycle and Publishing
knowl:
  id: concepts/release-and-publishing
  source_refs:
    - wiki-filesystem:knowl-docs/publishing.md@a37755a2d562c9f596a41e474422161f29278ba9d9e527e3375224a8ce4265b4
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
# Release Lifecycle and Publishing

Knowl releases are built, verified, and published through `.github/workflows/release.yml` when an annotated `vX.Y.Z` tag is pushed for a commit on `main`. The Go toolchain version is determined by `go.mod`, while the native npm target matrix and package identities are defined in `.omnidist/omnidist.yaml`. See [Knowl Overview and Operating Modes](knowl-overview.md) and [Agent Maintainers and Provider Configuration](agent-maintainers.md).

## Exporting Portable OKF Bundles and `llms.txt`

Operators can export the canonical semantic wiki for external distribution or static hosting without exposing operational state or raw evidence:

- `knowl export okf`: Copies the canonical portable OKF v0.2 wiki bundle while excluding `raw/` evidence, operational state, configuration files, and SQL databases.
- `knowl export llms-txt`: Renders a bundle-relative `llms.txt` navigation document to stdout or a selected file, supporting optional title, summary, and public base URL configuration.

## npm Trusted Publishing (`@omnidist/omnidist`)

Omnidist configures `publish-auth: trusted` and repository URL `git+https://github.com/baldaworks/knowl.git`. The CI publishing job runs with `id-token: write` and installs `npm 12.2.0`, requiring no npm token secret in GitHub Actions.

### Trusted Publisher Package Matrix

An npm package owner must register GitHub Actions as a trusted publisher (organization `baldaworks`, repository `knowl`, workflow filename `release.yml`, no environment name, allowing direct `npm publish`) for all six packages (all declaring MIT license metadata):

- `@baldaworks/knowl` (launcher package)
- `@baldaworks/knowl-darwin-x64`
- `@baldaworks/knowl-darwin-arm64`
- `@baldaworks/knowl-linux-x64`
- `@baldaworks/knowl-linux-arm64`
- `@baldaworks/knowl-win32-x64`

### One-Time Trust Binding Registration

From an authenticated npm owner account (with existing published packages, owner write access, and account 2FA enabled—any previously unpublished package requires an initial authenticated publish before registering trust bindings), inspect and apply the bindings derived from `.omnidist/omnidist.yaml`:

```bash
npx -y @omnidist/omnidist@latest --profile default npm trust \
  --repo baldaworks/knowl --workflow-file release.yml
```

Pass `--apply` to register the reviewed trust bindings.

## Cutting, Verifying, and Publishing a Release

1. **Pre-release alignment and checks**: Align the checked-in plugin, skill contracts, release metadata, and installation guides with the target version `X.Y.Z`. Run the release verification CLI, repository tests, and linters before merging into `main`:
   ```bash
   go run ./internal/cmd/releasecheck --version X.Y.Z
   ```
   Rollout validation gates cover module and generated OpenAPI binding verification, the full race-enabled Go test suite, vulnerability and architecture checks, strict `golangci-lint`, integration-tagged PostgreSQL source tests, sidecar/config parsing, source-layer/packaging audits, and a release-shaped persistent-volume sidecar smoke test.
2. **Tagging**: Create and push an annotated `vX.Y.Z` tag on the merged `main` commit.
3. **Workflow execution and artifacts**: `.github/workflows/release.yml` runs verification jobs, stages the five native npm platform packages and launcher (`@baldaworks/knowl`), and publishes only after all verification gates pass. It also publishes and attests:
   - The multi-platform Linux GHCR container image (`amd64` and `arm64`, e.g., `ghcr.io/baldaworks/knowl:vX.Y.Z`)
   - The GitHub Release recording the immutable container image digest
4. **Post-release verification and tag immutability**: Confirm workflow completion and verify the actual published versions in the registries before reporting success. Preserve failed-run logs and verified artifacts. Never move an existing public `vX.Y.Z` tag to another commit; cut a new patch version whenever a source or workflow fix is needed.

## Container Image Deployment, Upgrade, and Rollback

Release workflows append the immutable manifest digest to the GitHub Release; production deployments should pin `ghcr.io/baldaworks/knowl@sha256:<published-digest>` rather than a mutable version tag. See [Service Configuration, HTTP/MCP Transports, and Operator API](service-configuration-and-transports.md) and [Workspace Semantics and Source Lifecycle](workspace-and-source-lifecycle.md).

```bash
docker pull ghcr.io/baldaworks/knowl:v0.1.0
docker run --rm \
  -p 127.0.0.1:8080:8080 \
  -e KNOWL_OPERATOR_TOKEN=replace-with-a-strong-secret \
  -v knowl-data:/var/lib/knowl \
  ghcr.io/baldaworks/knowl:v0.1.0
```

- **Volume and Readiness**: Persist `/var/lib/knowl`, which owns the canonical raw/Markdown workspace and SQLite operational state, and mount configured filesystem source roots read-only. Starting with `v0.3.0`, a runnable `Host` requires either an explicitly injected maintainer or a valid `knowl.provider` selected from `runtime.providers` before readiness. `GET /readyz` succeeds only after workspace recovery, durable-work resumption setup, and projection preparation; individual source sync failures do not degrade `/readyz` (the host continues serving the last successful snapshot).
- **Upgrade Procedure**:
  1. Stop active writers and back up or snapshot the persistent volume and operational store per local policy.
  2. For legacy pre-v0.2.0 workspaces (including legacy `wiki/notes/**`), run the explicit, journaled `knowl migrate okf-v0.2` conversion before starting normal service operations.
  3. Ensure `runtime.providers`, `knowl.provider`, and any remote Git source settings (external credentials, `known_hosts`, repository identity, and rewrite policy) are configured. Stop the running container without deleting `/var/lib/knowl` and start the new release by immutable digest (`@sha256:<published-digest>`) with the updated config, read-only source mounts, volume, and operator token.
  4. Wait for `/readyz`, run `knowl run` or `knowl source sync --all` (which also backfills legacy `source_document` provenance and rebuilds projections even when maintenance operations are already terminal), inspect `knowl source status <id>` for policy-triggered work, diagnostics, or failures requiring manual class-filtered retry, optionally run `knowl hierarchy reconcile` and `knowl validate`, and verify authenticated unfiltered and `--source` filtered `retrieve` and `operation` reads.
- **Rollback Procedure**: Stop the new container and restore the previous known-good image digest and config together against the retained `/var/lib/knowl` volume. Do not discard pending operations, delete `raw/` or `.knowl/`, or run destructive down migrations; additive operational schema and raw revisions are retained for forward recovery. Rolling back across the `v0.5.0+` maintenance-generation/diagnostics operational migrations or after running the first `knowl hierarchy reconcile` operation (`v0.4.0+`) requires restoring the pre-upgrade operational database. Once gradual source retirement (`v0.7.0+`) begins, durable lifecycle and reconciliation state require the matching binary—prefer a forward fix over an in-place downgrade, because pre-`v0.7.0` binaries do not enforce retiring-source publication authority (while canonical Markdown and accepted `raw/` source history remain unchanged and portable; see [Workspace Semantics and Source Lifecycle](workspace-and-source-lifecycle.md) and [Retrieval and Hybrid Search Architecture](retrieval-and-search.md) for additional migration rollback constraints).

## Historical Release Notes

### v0.7.1 — Web UI Playwright Harness Stabilization and `0.7.1` Package Alignment

`v0.7.1` stabilized the browser test harness used to verify Knowl's Web UI while keeping application behavior and the durable storage schema unchanged from `v0.7.0`:

- **Browser Test Harness Fix**:
  - Keeps a pass-through Playwright route active while one-shot fault routes expire, preventing a Chromium interception shutdown race from stranding the UI's immediate recovery request after `snapshot_changed`.
  - Preserves fault injection, UI assertions, security checks, and existing deadlines without adding retries, increasing timeouts, or changing production UI code.
- **Release Contract Alignment and Upgrade**:
  - Aligned the Codex plugin, MCP launcher, installation guidance (`npx --yes @baldaworks/knowl@0.7.1 setup codex`; see [Agent Maintainers and Provider Configuration](agent-maintainers.md)), and native npm packages (`darwin-x64`, `darwin-arm64`, `linux-x64`, `linux-arm64`, and `win32-x64`, plus the Linux `amd64`/`arm64` container) at `0.7.1`.
  - Introduces no additional storage migration or configuration change beyond `v0.7.0` (upgrades from versions older than `v0.7.0` follow the `v0.7.0` upgrade and source-retirement guidance below).

### v0.7.0 — Gradual Source Retirement, Rejection Diagnostics, and Azure SSH Git Sync

`v0.7.0` added gradual reconciliation when configured sources are removed or replaced, preserved content rejection diagnostics, and fixed Azure SSH Git wiki synchronization:

- **Gradual Configured-Source Retirement and Replacement**:
  - Configured source owners transition through `active`, `retiring`, and `deleted`. Removing a source schedules bounded, durable page reconciliation: pages supported only by that owner are deleted, whereas mixed-source pages are recomputed from their remaining active evidence while keeping immutable `raw/` history available. Retirement completes only after an authoritative check confirms no remaining page references (see [Workspace Semantics and Source Lifecycle](workspace-and-source-lifecycle.md)).
  - Replacing a filesystem source with Git creates an independent source owner rather than a citation alias; operators must accept the replacement source before removing the old one, and reconciliation proceeds gradually without promising an atomic global cutover.
- **Rejection Diagnostics, Azure SSH Git Sync, and Repository Wiki Updates**:
  - Rejected content plans retain page paths, rule codes, and dependency reasons—including when no safe changes remain (`plan.empty_safe_subset`)—while continuing to withhold invalid content from publication.
  - Git synchronization supports Azure SSH wiki repositories (`ssh.dev.azure.com`) and resolves fetched commits accurately.
  - The repository's own wiki was updated with local Codex guidance and nested navigation catalogs while preserving existing accepted revisions and update history.
- **Packaging (`0.7.0` Historical Recipe) and Upgrade Constraints**:
  - Aligned the local Codex plugin, MCP launcher, and five native npm packages to `0.7.0` with historical bootstrap `npx --yes @baldaworks/knowl@0.7.0 setup codex` (superseded by `@baldaworks/knowl@0.7.1` in current documentation; see [Agent Maintainers and Provider Configuration](agent-maintainers.md)).
  - Source retirement introduces durable lifecycle and reconciliation state requiring a matching binary; once retirement begins, prefer a forward fix over an in-place downgrade because older binaries do not enforce retiring-source publication authority.

### v0.6.1 — npm Trusted Publishing Rollout and Accumulated Post-v0.5.0 Capabilities

`v0.6.1` prepared the local Codex plugin and five native CLI packages (`darwin-x64`, `darwin-arm64`, `linux-x64`, `linux-arm64`, and `win32-x64`) for publication through npm trusted publishing, rolling up all changes accumulated since `v0.5.0` after the `v0.6.0` publication stopped before artifact upload because its workflow required an npm token:

- **OIDC Trusted Publishing and `0.6.1` Package Alignment**:
  - Switched npm publication to GitHub Actions OIDC through Omnidist with no stored npm publication token, requiring each package's trusted publisher binding to be registered for `baldaworks/knowl` and workflow `release.yml`.
  - Aligned the local Codex plugin (`setup` and `run` skills with host-owned MCP stdio integration), launcher, and native packages to version `0.6.1`, with historical local bootstrap `npx --yes @baldaworks/knowl@0.6.1 setup codex` (superseded by `@baldaworks/knowl@0.7.1` in current documentation; see [Agent Maintainers and Provider Configuration](agent-maintainers.md)).
- **Accumulated Feature Rollup Since `v0.5.0`**:
  - **Optional Operator Web UI**: Added read-only knowledge browsing, search, processing history, source status, and saved-source inspection (see [Service Configuration, HTTP/MCP Transports, and Operator API](service-configuration-and-transports.md)).
  - **Complete-Page Hybrid Search and Embedding Reuse**: Expanded hybrid search to cover complete pages while preserving structured Markdown and OKF section context, and reused authenticated embeddings when rebuilding SQLite or PostgreSQL projections (see [Retrieval and Hybrid Search Architecture](retrieval-and-search.md)).
  - **Portable Navigation and Graph Extraction**: Updated generated wiki navigation to use portable relative Markdown links, validated generated navigation during maintenance, preflighted exported OKF documents, and excluded literal code and image descriptions from link graph extraction.
  - **Repository Wiki Regeneration**: Regenerated and independently reviewed the repository's own wiki with the configured maintainer while preserving existing `raw/` revisions and log history.
- **Release Verification Gates**:
  - Gated publication on module and OpenAPI checks, the full race-test suite, vulnerability scanning, both linters, PostgreSQL integration tests, persistent-volume container smoke tests, and verified native npm artifacts, publishing the multi-platform container and recording its immutable digest.

### v0.6.0 — Local Codex Plugin and Staged npm Packages

`v0.6.0` added the repository-supplied local Codex plugin and staged the Knowl CLI for npm as `@baldaworks/knowl@0.6.0` alongside five scoped native packages (`darwin-x64`, `darwin-arm64`, `linux-x64`, `linux-arm64`, and `win32-x64`), all declaring MIT license metadata (see [Agent Maintainers and Provider Configuration](agent-maintainers.md) for the current `v0.7.1` local Codex workflow):

- **Local Codex Bootstrap and Skills (`v0.6.0` Historical Recipe)**:
  - Supported local project root bootstrap for `v0.6.0`: `npx --yes @baldaworks/knowl@0.6.0 setup codex` (superseded by `@baldaworks/knowl@0.7.1` in current documentation).
  - The plugin includes only the `setup` and `run` skills and registers the three Knowl MCP tools (`knowl_retrieve`, `knowl_ingest`, `knowl_operation`) through host-owned MCP stdio without a local HTTP daemon, port, operator token, or separate `codex mcp add`. Explicit HTTP sidecar and embedded-Go deployments remain supported.
  - In `v0.6.0`, the `run` skill accepts a local CLI only when its structured version identity is exactly `0.6.0`, otherwise keeping the entire run on the pinned `@baldaworks/knowl@0.6.0` npm launcher. Project maintenance still requires the configured maintainer provider.
- **Marketplace Fetching, Offline Prewarming, and Verified Scope**:
  - `setup codex` can fetch both npm artifacts and a pinned, sparse Git marketplace; sparse checkout limits materialized paths rather than guaranteeing a metadata-only fetch, so offline environments must be prewarmed while those exact artifacts are reachable.
  - Verified scope covers local Codex with project state on a local filesystem (concurrent local processes for one project coordinate the durable store and workspace while separate project working directories remain isolated). Hosted/remote plugin execution, remote MCP hosting, and shared/network filesystems are not supported.
- **Repository Staging vs. Publication**:
  - The `v0.6.0` release note records that the npm artifacts and plugin were implemented and verified in the repository without claiming prior registry publication; actual publication requires the authorized release workflow, a `v0.6.0` tag on `main`, green repository/integration/container/npm checks, and npm publish authority for `@baldaworks/knowl` and its native packages (as noted in `v0.6.1`, the `v0.6.0` publication run stopped before artifact upload because its workflow still required a token).

### v0.5.0 — Policy-Aware Maintenance and Isolated Validation

`v0.5.0` (`ghcr.io/baldaworks/knowl:v0.5.0` for Linux `amd64` and `arm64`) introduced policy-generation maintenance tracking, open-world source link preservation, and safe-subset provenance isolation (see [Maintenance Contract and Execution](maintenance-contract-and-execution.md)):

- **Policy-Aware Source Maintenance**:
  - A deterministic, non-secret policy generation participates in durable maintenance identity. Changing an output-affecting rule (such as the effective read ceiling) makes unchanged source revisions eligible for one new bounded maintenance cycle (allowing documents that previously failed the `32,768`-character limit to be reconsidered under the `262,144`-character default without editing the source), while repeated scans under the same generation converge without duplicate work.
  - Automatic reconsideration is restricted to safe source-policy transitions; `staging`, `provider`, and unknown failures retain their manual retry gate.
- **Open-World Links and Provenance Isolation**:
  - Unresolved links proven to exist in accepted source Markdown are preserved and reported via bounded, redacted diagnostics rather than failing staging, whereas newly invented unresolved links, malformed paths, and escaping targets remain rejected.
  - Invalid Knowl-owned provenance (such as `citation.unknown_source`) is isolated to the offending document and its smallest deterministic dependent catalog closure, allowing valid sibling documents to commit while preserving the prior canonical version of the rejected document.
- **Durable Store and Git Cache Hardening**:
  - Added SQLite and PostgreSQL operational migrations for maintenance generation and bounded diagnostics (with explicit legacy-row replay compatibility for empty pre-diagnostics state; binary rollback across these migrations requires restoring the pre-upgrade operational database).
  - Hardened Git cache lineage and repository identity checks to reject stale, rebound, or structurally incomplete cache state.

### v0.4.0 — Connected Sources and Publishable Wikis

`v0.4.0` (`ghcr.io/baldaworks/knowl:v0.4.0` for Linux `amd64` and `arm64`) expanded the source-to-publication workflow with remote Git sources, one-shot execution and retry recovery, explicit hierarchy reconciliation, and portable bundle exports:

- **Remote Git Sources**: Added remote Git repository synchronization over HTTPS and SSH alongside filesystem sources, with external credential configuration, fail-closed validation of SSH `known_hosts`, repository identity, ref movement, cache size, and pack transfer limits, explicit operator acknowledgement for branch rewrites/moved tags/repository rebinding, and credential/content redaction in logs and errors.
- **One-Shot Execution and Failure Recovery**: Added `knowl run` (bounded source sync, maintenance drain, and optional hierarchy reconciliation without keeping HTTP running) and `knowl source retry` (previewing and requeuing failed maintenance by stable failure class while preserving the last successful source checkpoint and searchable projection).
- **Semantic Hierarchy and Export Commands**: Added `knowl hierarchy reconcile` (deterministic, bounded, staged, conflict-checked subject-first catalog organization), `knowl export okf`, and `knowl export llms-txt`.
- **Retrieval, Ingestion, and Store Updates**:
  - Indexed semantic OKF tags and added fallback to native relaxed (OR) matching when strict (AND) queries return no results.
  - Increased the default wiki document character ceiling from **32,768** to **262,144 characters** (alongside the independent **4 MiB** byte guard), accepting documents at the limit and failing larger documents before provider invocation.
  - Added additive operational-store migrations for generic hierarchy operations, semantic tag projection, retry state, and Git repository identity.

### v0.3.1 — Legacy Provenance Backfill

`v0.3.1` (`ghcr.io/baldaworks/knowl:v0.3.1` for Linux `amd64` and `arm64`) fixed source-filtered retrieval for workspaces whose raw revisions were accepted before configured-source provenance was stored in raw manifests:

- **Provenance Backfill and Projection Rebuild**:
  - During reconciliation, Knowl enriches legacy raw manifests with their validated `source_document` identity while reusing already accepted immutable source bytes (without re-fetching unchanged documents from the connector).
  - Enrichment is one-way and conflict-safe: existing provenance cannot be overwritten by a different document identity.
  - Running `knowl source sync --all` once after deploying `v0.3.1` backfills legacy provenance and rebuilds the projection even when all maintenance operations are already terminal, recording `source_documents` and page-to-source relations so `knowl retrieve --source <id>` returns the same relevant semantic pages as unfiltered retrieval.
  - Bootstrap and `sync.on_start` remain optional, and source documents and legacy `wiki/sources/<source_id>/**` mirrors remain excluded from the portable semantic wiki.

### v0.3.0 — Semantic Source Maintenance

`v0.3.0` transitioned configured sources from read-only `wiki/sources/**` mirrors into immutable `raw/` evidence synthesized into a single maintainer-owned semantic wiki (`ghcr.io/baldaworks/knowl:v0.3.0` for Linux `amd64` and `arm64`):

- **Breaking Changes (Relative to `v0.2.0`)**:
  - A runnable `Host` requires an explicitly injected maintainer or a valid `knowl.provider` selected from `runtime.providers` before readiness.
  - Filesystem synchronization stores exact accepted revisions under `raw/` and no longer materializes active `wiki/sources/<source_id>/**` mirrors. On each source's next successful reconciliation, Knowl removes only that source's legacy `wiki/sources/<source_id>/**` subtree via staged recovery while preserving `raw/` revisions and curated semantic pages.
  - A successful source sync represents raw acceptance and durable maintenance reservation; model-backed wiki updates complete asynchronously and are tracked via `knowl source status` (which exposes bounded `queued`, `replayed`, `committed`, and `failed` counts and samples). `sync.on_start` and `bootstrap` remain optional.
- **Semantic Wiki and Provenance Behavior**:
  - The maintainer creates and updates root-reachable semantic OKF `entities/`, `concepts/`, `syntheses/`, and catalogs rather than copying source paths.
  - Curated factual pages cite accepted raw refs, preserve unrelated source lineages, and replace an older revision only within the same source-document lineage. Upstream document deletion tombstones source state without implicitly deleting accumulated wiki knowledge.
  - Multiple configured sources can support a single page; retrieval returns the page's sorted `source_documents` collection and matches the shared page when filtering by any contributing source. SQLite and PostgreSQL share the same rebuildable page-to-source projection, and catalog/control pages are excluded from retrieval evidence.
  - Structured logs correlate source, document, revision, and operation identifiers without logging source bodies, prompts, credentials, or provider secrets.

### v0.2.0 — Multi-Source Wiki Sync

`v0.2.0` introduced the multi-source filesystem sync engine and the canonical OKF v0.2 (`okf-v0.2`) bundle representation:

- **Proven Capabilities**:
  - Named sources use `(source_id, document_id, revision)` identity so equal relative paths across sources do not collide. Initial, unchanged, selective update, complete deletion, interrupted scan, restart, and source-failure isolation converge through one production `Host`.
  - Unchanged documents are not rewritten; deleted documents are tombstoned after a complete scan while retaining immutable raw history.
  - Retrieval supports optional source filters and returns `source_id`, `document_id`, `revision`, canonical URI, title, and bounded snippet evidence.
  - CLI commands (`knowl source list`, `source sync <id>`, `source sync --all`, `source status <id>`), fresh-workspace bootstrap (`knowl bootstrap wiki|obsidian|okf <path>`), and explicit migration (`knowl migrate okf-v0.2`) share the same engine as embedded `Host` calls.
  - Structured OKF metadata is preserved across all surfaces while Attested Computation declarations remain inert metadata.
  - Checked-in sidecar examples (`engineering` and `operations` fixtures) demonstrate on-start sync, 5-minute intervals, and capped retry.
- **Historical v0.2.0 Behavior and Scope Limits (Superseded in `v0.3.0` and Later)**:
  - **Provider-free `Host` startup and `wiki/sources/<source_id>/**` mirrors**: In `v0.2.0`, `Host` startup and source sync required no maintainer provider (provider-free public ingest returned `maintainer_unavailable`) and materialized read-only mirrors under `wiki/sources/<source_id>/**` without cross-source synthesis. Both behaviors were superseded in `v0.3.0` (see above and [Workspace Semantics and Source Lifecycle](workspace-and-source-lifecycle.md)).
  - **Historical limits later lifted**: `v0.2.0` had no Git source adapter, automatic cross-source synthesis, export/publish CLI (`knowl export`), embeddings, or operator UI (all added in later releases). It remained pull-only with no write-back or source-management HTTP/MCP tools.

### v0.1.0 — Crash-Safe Knowledge Loop

`v0.1.0` was the first release of the self-hosted knowledge sidecar:

- **Proven Baseline Capabilities**:
  - Accepted immutable sources and execution descriptors survive restart; a single application runner resumes work to a terminal `completed` or `failed` state with at-least-once execution and idempotent canonical commit.
  - Markdown and immutable raw sources serve as canonical state, while SQLite/PostgreSQL and lexical search remain rebuildable projections.
  - Relevance-ordered lexical search returns match-centered bounded excerpts, source-aware maintenance updates existing decisions instead of duplicating them, and the deterministic `project-decisions` corpus achieved **12/12** expected top-five retrieval hits across MCP, HTTP, SQLite, and PostgreSQL with source references.
  - Public business contract fixed at three operations (`knowl_retrieve`, `knowl_ingest`, `knowl_operation` and HTTP equivalents) protected by the operator bearer token, with unauthenticated `/healthz` and `/readyz` probes.
- **Historical v0.1.0 Scope Limits (Partially Superseded in Current Contract)**:
  - At `v0.1.0`, Knowl had no operator web UI, embeddings/hybrid search, or remote Git source synchronization (all three were added in subsequent releases; see [Knowl Overview and Operating Modes](knowl-overview.md), [Retrieval and Hybrid Search Architecture](retrieval-and-search.md), and [Workspace Semantics and Source Lifecycle](workspace-and-source-lifecycle.md)).
  - Remaining non-goals from `v0.1.0`—no connector zoo, multi-tenancy, automatic Git push/sync of the generated workspace, generic memory API, or section-level provenance—continue to apply in the current contract.
