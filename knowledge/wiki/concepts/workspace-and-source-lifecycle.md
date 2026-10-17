---
type: concept
title: Workspace Semantics and Source Lifecycle
knowl:
  id: concepts/workspace-and-source-lifecycle
  source_refs:
    - wiki-filesystem:knowl-docs/design.md@c9a981be23b39a137da77ad85726d8382d33306b44e801022bcdd4de4b525132
    - wiki-filesystem:knowl-docs/examples/own-docs-wiki.md@4b076ff94b528e7e337477c9190b8aa2b59649bb07e78bd65db880b1b28c8fa2
    - wiki-filesystem:knowl-docs/local.md@b1186f32503e604c07be2a94377577eb78e1a3b883ea074ec4d8415d0030abdc
    - wiki-filesystem:knowl-docs/operations.md@ae3c7b6ee1583fd88681e5a3ee4bb1cdf2a9b007235663169cf7e59a41b60baa
    - wiki-filesystem:knowl-docs/workspace.md@71295f092aa67b4f9e1d6c21db3f14f596f6319b940a51102d256747c96f093b
---
# Workspace Semantics and Source Lifecycle

The filesystem workspace is the canonical content owner in Knowl, while SQL and search state are operational and rebuildable projections. See [Knowl Overview and Operating Modes](knowl-overview.md) and [Service Configuration, HTTP/MCP Transports, and Operator API](service-configuration-and-transports.md).

## Canonical Filesystem Layout and Trust Boundaries

- `schema.md`: Required, operator-owned Markdown policy (`schema_version: 1`; older files without a header default to `1`) supplied to the maintainer as untrusted guidance rather than an executable validation DSL. Maintainer plans may read `schema.md`, but edit validation rejects schema modifications. Its exact byte digest is recorded with every operation and rechecked before staging and commit so policy changes invalidate plans built against older bytes.
- `raw/<scope/source identity>/<version>/`: Stores immutable accepted source bytes (`source`) and structured metadata (`manifest.yaml`, referenced by `manifest_ref`). The filesystem adapter safely tokenizes path components so physical directories need not equal the source ID; logical identity is always `(scope, adapter, source ID, version)`. Replaying the same digest is idempotent, whereas the same identity/version with a different digest is rejected as a conflict. Accepted manifests carry `source_document` (`source_id`, `document_id`, immutable `revision`, and canonical `uri`).
- `wiki/`: Stores only the human-readable, directly portable semantic OKF v0.2 bundle maintained by Knowl (`entities/*.md`, `concepts/*.md`, `syntheses/*.md`, `catalogs/<semantic-domain>/index.md`, `index.md`, and `log.md`) without configured-source copies.
- `.knowl/`: Implementation state (`.knowl/staging/` and `.knowl/recovery/` for preview, atomic commit, and interruption recovery, plus rebuildable `.knowl/knowl.sqlite` state). Do not hand-edit staging or recovery files while a host is running.
- **Initialization and Validation**: `knowl init` creates the required directories and starter `schema.md`, `wiki/index.md` (`okf_version: "0.2"`), and `wiki/log.md` without overwriting existing files. `knowl validate` checks required workspace paths, while full migration, projection, and recovery checks run during `knowl start` or `Host` construction.

### Page Contract, Cross-References, and Control Pages

- **OKF Frontmatter and Provenance**: Ordinary pages are OKF concepts whose safe bundle-relative path without `.md` matches `knowl.id`. `type` is required; `title`, `description`, `resource`, `tags`, `sources`, lifecycle, generation, and verification fields follow OKF v0.2, and unknown producer fields round-trip as OKF extensions. `knowl.source_refs` holds stable citations (`adapter:source-id@version`); snapshots resolve supporting refs into a sorted `source_documents` collection without copying raw bodies into the page.
- **Cross-References**: Curated pages use standard Markdown links with explicit `.md` destinations relative to the originating page (URL-encoding characters when needed). Generated navigational double-bracket links, bundle-root paths, and escaping paths are rejected (literal code examples are preserved; imported OKF may retain bundle-relative links and legacy wikilinks remain readable). Missing article targets produce navigation warnings rather than OKF structural failures or concept-graph edges; external URLs and assets are excluded from the concept graph, while catalogs require existing targets. Maintainer plans may target safe semantic `wiki/**/*.md` except `wiki/log.md` and the reserved legacy `wiki/sources/**` boundary.
- **Control Pages (`wiki/index.md`, `catalogs/**/index.md`, `wiki/log.md`)**:
  - `wiki/index.md` is the OKF root catalog declaring `okf_version: "0.2"`. Nested `index.md` files are reserved catalogs and cannot redeclare the bundle version. Catalog controls are validated and included in canonical digests but excluded from retrieval evidence.
  - `wiki/log.md` is an application-maintained, newest-first ISO-date-grouped OKF audit control (never editable by maintainer plans; its prior digest is part of staging/commit preconditions). Each committed operation appends one structured JSON metadata line (`operation ID`, `generation`, `schema digest`, `cited source references`, and historical `committed file paths`, including deletions). Log lint identifies commits by `(operation ID, generation)`: an identical repeated record produces a warning, whereas conflicting metadata for the same identity produces an error. Source OKF logs remain in `raw/` and are not imported.
- **OKF Attested Computation declarations**: Treated purely as inert data (`runtime`, `parameters`, `computation`, `executor`, `attester` are parsed, preserved, projected, and returned without dereferencing resources or executing code).

### Reading Pages, Accepted Sources, and Exports

- **Browser Inspection**: In the optional Web UI (see [Service Configuration, HTTP/MCP Transports, and Operator API](service-configuration-and-transports.md)), **Page sources** and **Read saved source** read immutable accepted text and digests from `raw/` without fetching upstream (reporting missing, oversized, or non-text revisions explicitly rather than substituting newer upstream files). **Open original** appears only for safe, credential-free HTTP/HTTPS provenance URIs with userinfo, query strings, and fragments stripped (carrying no operator token). **Open current page** from search reads the current canonical page; the browser does not store historical wiki-page versions.
- **Exporting Portable OKF Bundles and `llms.txt`** (see [Release Lifecycle and Publishing](release-and-publishing.md)):
  ```bash
  knowl export okf --output ./public
  knowl export llms-txt --output ./public/llms.txt
  knowl export llms-txt --base-url https://docs.example.com/knowledge/ --output ./llms.txt
  ```
  `knowl export okf` requires a non-existent `--output` directory and copies `index.md`, `log.md`, catalogs, pages, and bundle assets (excluding `schema.md`, `raw/`, `.knowl/`, configs, and DBs; there is no `export wiki` alias or archive compression). `knowl export llms-txt` writes bundle-relative links by default (or stdout when `--output` is omitted) and supports `--base-url`, `--title`, and `--summary` following the llms.txt v2 proposal.

### Startup Recovery, Canonical Migrations, and Git Version Control

- **Recovery Journal**: Content commits write a staging manifest under `.knowl/staging/`, record preimages in `.knowl/recovery/`, atomically replace target files, and mark the journal committed. Before projection readiness on startup:
  - A `prepared` journal restores preimages and records a rolled-back operation.
  - A `committed` journal is cleaned once canonical files are complete.
  - Incomplete staging is discarded as uncommitted work.
- **Canonical OKF v0.2 Migration**: Explicit and idempotent via `knowl migrate okf-v0.2` followed by `knowl validate` (stop active writers and back up first). Preflights and journals the conversion, archives the exact legacy log, writes its completion marker last, and rebuilds projections. Startup and read-only commands reject legacy canonical state rather than mutating it implicitly.
- **Hierarchy Operational Migration**: Additive in SQLite and PostgreSQL, leaving existing source operation rows unchanged. Downgrading to an older binary is safe only before any hierarchy operation row exists; after the first reconcile, restore the pre-upgrade operational database.
- **Git Versioning and Backups**: Canonical writes preserve single-writer ordering under one `Host` and in-process coordinator. Knowl never commits, pushes, or uploads the workspace; operators commonly version `schema.md`, `raw/`, and `wiki/` in Git while keeping `.knowl/` as local operational state (if versioning `.knowl/`, exclude credentials and review database/staging files). For backups, stop the host first and copy the complete workspace.

## Configured Sources: Filesystem, Remote Git, and Bootstrap

### Filesystem and Remote Git Source Configuration

Sources are configured under `knowl.sources` with `type: filesystem` (flavors: `markdown`, `obsidian`, `okf`) or `type: git`, plus optional `sync` scheduling (`on_start`, `interval`, `retry_initial`, `retry_maximum`). `on_start` attempts run asynchronously; each enabled source has independent interval and capped retry state and cannot overlap itself.

Git sources use the same `source list`, `source sync`, `source status`, scheduling, retry, and maintenance lifecycle as filesystem sources:

- **Protocols and Credentials**: Read-only over HTTPS or SSH, pinning each complete scan to one immutable commit (`ref` and `ref_kind: branch | tag`). Credentials must be external via `auth.secret_env` (HTTPS token or SSH private key) or `auth.key_file` (SSH key file); SSH also requires host-bound public keys in `known_hosts`. Never place secrets in `remote`, `uri_base`, or `repository_id`.
- **Fast-Forward and Rebind Guards**: By default, Knowl rejects non-fast-forward branch changes and moved tags while preserving the last successful checkpoint and active catalog. Set `allow_rewrite: true` to permit rewritten branch history. Set `rebind_ack: true` only for a sync that intentionally adopts a new repository identity or moved tag, then remove it.
- **Scoped Bare Cache and Transfer Limits**: Git data under `<workspace>/.knowl/cache/git/<source-id>` is a rebuildable scoped bare cache fetching only the tracked ref (retaining its full history/objects without unrelated branches or tags; `include` patterns filter paths post-transfer). Default limits cap one pack transfer at **500 MiB** (`max_transfer_bytes: 524288000`) and one source cache at **512 MiB** (`max_cache_bytes: 536870912`), configurable up to **8 GiB**. Azure DevOps SSH sources (`ssh.dev.azure.com`) negotiate `multi_ack_detailed` and request a complete pack on changed refs.
- **Fallback Provenance URIs**: `knowl://sources/...` URIs escape Unicode, spaces, or invalid percent escapes, while preserving existing canonical provenance URIs on replay.

### Bootstrap Preflight vs. Ordinary Source Sync

```bash
./knowl bootstrap wiki /path/to/wiki
./knowl bootstrap obsidian /path/to/vault
./knowl bootstrap okf /path/to/okf-bundle
```

- **Bootstrap**: Enforces a fresh-workspace guard and performs one shared source sync using source ID `bootstrap-wiki`, `bootstrap-obsidian`, or `bootstrap-okf`. The `okf` flavor validates and preserves OKF v0.2 metadata, reserved controls, Unicode paths, and standard concept links (missing version defaults to `v0.2`; other versions are consumed best-effort and reported under `diagnostics`). If an operator-owned config already exists, bootstrap does not rewrite it.
- **Ordinary Source Sync**: Accepts existing workspaces, stores source revisions only in `raw/`, reserves idempotent maintenance work for changed text, and never copies source content into `wiki/`.

### CLI Source Controls and One-Shot Execution

```bash
./knowl source list
./knowl source sync engineering
./knowl source sync --all
./knowl source status engineering
./knowl run [--source <id>] [--no-sync] [--no-hierarchy]
```

`source status` reports durable last-attempt/last-success scan state alongside bounded maintenance counts (`queued`, `retrying`, `replayed`, `committed`, `failed`) and operation samples (`generation_prefix`, `work_attempt`, `retry_attempt`, `manual_retry_count`, `failure_class`, safe `failure_reason`, and `next_retry_at`).

## Explicit Semantic Hierarchy Reconciliation (`hierarchy-v3`)

To replace a flat root with source-independent semantic catalogs, stop other writers and run:

```bash
./knowl hierarchy reconcile
./knowl validate
```

- **Execution and Scope**: The only hierarchy-specific mutation command (startup, bootstrap, validate, retrieve, operation reads, source list/status, and source sync never reconcile catalogs). Reserves and claims one durable hierarchy operation, stages catalog changes with schema/snapshot/target digest preconditions, commits via the recovery journal, appends `wiki/log.md`, updates projections, and validates the workspace without starting HTTP, the general scheduler, source jobs, or `on_start` sync. Returns structured JSON (`operation_id`, `status`, `changed`, and—when changed—`generation` and `files`; `"changed": false` on a converged replay).
- **Planner Identity and Inputs**: Uses `hierarchy-v3` and effective output policy. Consumes deterministically ordered, bounded page metadata (`id`, `path`, `digest`, `type`, `title`, `description`, `tags`), user-content excerpts, current catalog memberships, and the schema digest (raw source bodies, `schema.md` prose, frontmatter/provenance envelopes, and source-native paths are excluded). Owns only `wiki/index.md` and generated `wiki/catalogs/**/index.md`, leaving ordinary pages, `wiki/log.md`, `raw/`, and operator-created catalogs elsewhere untouched. Every generated non-root catalog must be non-empty (an empty root is valid only for an empty wiki), and duplicate child edges within a single catalog are invalid.
- **Hierarchy Bounds**: All-or-nothing planning capped at **1,024 ordinary pages**, **1,024 catalogs**, **16,384 edges**, **depth 16**, **4 MiB** input, **4,096 excerpt characters** per page, **1 MiB** structured plan output, **1,024 managed edits**, **256 KiB** per catalog, and a **1 MiB** stage manifest.

## Provenance, Document Tombstoning, and Gradual Source Retirement

Every curated factual page cites accepted raw source references (`knowl.source_refs`). Deleting an upstream document within an active source creates a reconciled tombstone after a complete scan (interrupted scans never authorize deletion) without erasing accumulated wiki knowledge, and removes any legacy `wiki/sources/<source_id>/**` subtree on the next successful sync.

### Gradual Configured-Source Retirement Status and Recovery

Removing a configured source transitions its exact `adapter` + `source_id` owner from `active` to `retiring` and then `deleted` (for example, `wiki-filesystem:fastronome-wiki` and `git:fastronome-wiki` remain independent owners even when their IDs and documents match):

- **Readiness Gate**: If a configuration change both adds and removes source owners, all added owners must finish their accepted sync and maintenance-readiness checkpoint before retirement page work begins. Pure removals have no gate.
- **Gradual Page Reconciliation**: Unchanged pages may retain pre-transition retiring refs while awaiting their turn, but newly staged pages cannot introduce or retain retiring refs. Old-only pages are deleted with the smallest catalog closure needed to keep the OKF graph valid; mixed pages are recomputed from remaining active evidence while keeping their catalog membership. Owner status becomes `deleted` only after all page operations and projection replay are terminal and a fresh authoritative canonical scan finds zero refs.
- **Status Inspection and Diagnostics**: Inspect via `./knowl source status <id> --retirement-adapter <adapter>` or `GET /operator/v1/source-retirement?adapter=...&source_id=...`. Tracks page operation counts (`pending`, `planned`, `applying`, `projection_pending`, `committed`, `failed`), discovery cursor/EOF, round matches, and remaining-reference counts. Summary failure `plan.empty_safe_subset` retains attached per-page diagnostic codes (`citation.plan_missing`, `citation.unknown_source`, `citation.lineage_removed`, `citation.source_retiring`, `catalog.dependency_rejected`).
- **Source Authority Fence and Rollback**: Queued maintenance or catalog stages are rejected if their source retires before publication, or with `citation.source_authority_changed` if the owner's config changes or is reactivated. To roll back operationally, restore the removed source in configuration and let synchronization rebuild what is needed.
- **Discovery and Plan Bounds**: Each discovery call returns at most **32 affected pages**, examines at most **1,024 canonical entries**, and enforces a **100,000-page** round ceiling. Each page operation uses the **32-file**, **256 KiB-per-file**, and **64-ref** plan limits, with active retirement evidence capped at **64 refs** and **4 MiB**.
- **Retrying Failed Retirement Operations**:
  ```bash
  ./knowl source retry <id> --retirement-adapter <adapter> [--page-id <id>] --dry-run
  ```

## Project-Local CLI and Repository Own-Docs Workflows

### Local Quickstart (`codex_acp`)

The default SQLite binary bundles native cosine SQL (even in CGO-free builds). After configuring `.config/knowl/config.yaml` with `codex_acp` (`bridge_version: "1.7.3"`) and a `filesystem` source, operators run `./knowl init`, `./knowl run --source <id>`, `./knowl validate`, `./knowl source status <id>`, and `./knowl retrieve '<query>'`.

### Repository Own-Docs Wiki (`antigravity_acp`)

Knowl's repository maintains `knowledge/wiki/` and `knowledge/raw/` from `docs/` (`knowl-docs`) using `antigravity_acp` (`gemini-3.8-flash-medium`), SQLite under `knowledge/.knowl/`, and hybrid search (`127.0.0.1:8091` E5-base with lexical fallback) via `task wiki:generate` and `task wiki:validate`.
