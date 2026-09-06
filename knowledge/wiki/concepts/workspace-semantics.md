---
type: topic
title: Workspace Semantics
knowl:
  id: concepts/workspace-semantics
  source_refs:
    - wiki-filesystem:knowl-docs/workspace.md@89fdd16e6a6ec14735c40e9ef3b4be64028ae57da08ac27b9f722fcc72880cdb
---
# Workspace Semantics

The workspace is the canonical Knowl artifact. `wiki/` functions as a directly portable Open Knowledge Format (OKF) v0.2 bundle alongside immutable raw source files in `raw/`, with SQL databases acting solely as rebuildable operational indexes and projections.

## Ownership and Layout

Workspace structure divides ownership cleanly across distinct directories:

- `schema.md`: Operator-controlled policy defining page types, frontmatter requirements, and lint rules. Maintainer edit plans may read it, but edit validation strictly rejects schema changes.
- `raw/`: Stores immutable accepted source bytes and manifests (`manifest.yaml`) identified by the logical tuple `(scope, adapter, source ID, version)`. Replaying an existing digest returns the existing record; conflicting digests trigger an error. Physical directory names need not equal the source ID as path components are safely tokenized.
- `wiki/`: Accumulated semantic knowledge consisting of ordinary pages (`entities/`, `concepts/`, `syntheses/`), catalogs (`catalogs/`), and the append-only `log.md`. It contains no raw source mirrors and remains directly readable and Git-compatible.
- `.knowl/`: Houses implementation and ephemeral operational state (`staging/`, `recovery/`, and `knowl.sqlite`) used for preview, atomic commits, interruption recovery, and search indexing.

Workspace lifecycle commands include `knowl init` (creates initial directories, starter `schema.md`, `wiki/index.md`, and `wiki/log.md`) and `knowl validate` (verifies required paths and integrity constraints).

## Page Contract and Conventions

Ordinary wiki pages represent OKF concepts conforming to strict structural rules:

- **Path and Identity**: The bundle-relative path without the `.md` extension defines the concept identity (`knowl.id`).
- **Frontmatter**: Bounded YAML frontmatter specifies a required `type`, alongside OKF v0.2 metadata (`title`, `description`, `tags`, etc.). Unknown producer fields round-trip as OKF extensions.
- **Provenance Citations**: The namespaced `knowl.source_refs` extension records citations in `adapter:source-id@version` format; every maintained page must cite at least one accepted raw source. Snapshots resolve supporting refs into sorted `source_documents` collections.
- **Link Semantics**: Curated Knowl pages use double-bracket wiki links targeting existing bundle-relative page identities. Imported OKF concepts use standard Markdown links (e.g. `[Related](related.md)`). External URLs, assets, and unresolved targets are excluded from the internal concept graph.
- **Edit Boundaries**: Maintainer plans may modify semantic pages under `wiki/` but cannot target `wiki/log.md` or reserved legacy `wiki/sources/**` paths.
- **Tombstoning**: Upstream deletions tombstone documents only after a complete scan; interrupted scans never delete source documents or curated semantic pages.

## Control Pages and Audit Logging

- **Root and Nested Catalogs**: `wiki/index.md` is the root OKF catalog declaring `okf_version: "0.2"`. Nested `index.md` files serve as sub-catalogs. Catalogs structure navigation and are excluded from retrieval evidence.
- **Audit Log (`wiki/log.md`)**: Maintained strictly by the application as an append-only, newest-first ISO-date-grouped log. Each commit appends a JSON line detailing operation ID, generation, schema digest, cited source references, and affected file paths.
- **Attested Computation**: Associated metadata is parsed and preserved in projections, but never dereferenced or executed.

## Explicit Semantic Hierarchy Reconciliation

- **Reconciliation Command**: Explicitly invoked via `knowl hierarchy reconcile` using the `hierarchy-v3` subject-first planning contract to reorganize `wiki/index.md` and `wiki/catalogs/**/index.md`.
- **Scope and Bounds**: Reorganization owns only `wiki/index.md` and generated `wiki/catalogs/**/index.md`. Limits enforce a maximum of 1,024 ordinary pages, 1,024 catalogs, 16,384 edges, graph depth of 16, 4 MiB total input, 4,096 excerpt characters per page, 1 MiB output, 1,024 edits, 256 KiB per catalog, and 1 MiB stage manifest.
- **Isolation**: Hierarchy reconciliation stages catalog changes with preconditions and commits through the recovery journal without starting HTTP listeners, schedulers, or background sync jobs.

## Recovery and Git

- **Atomic Staged Commits**: Content updates write preimages to a recovery journal and stage files before atomic replacement. Interrupted operations deterministically rollback or complete on restart.
- **Startup and Shutdown Recovery**: A `prepared` journal restores preimages and records a rolled-back operation; a `committed` journal is cleaned after canonical files complete; incomplete staging is discarded. Recovery checks run at startup and repeat during shutdown.
- **Migration and Legacy Cleanup**: Legacy `wiki/sources/<source_id>/**` mirrors are deleted through staged recovery during reconciliation while preserving raw revisions. Legacy workspaces migrate explicitly via `knowl migrate okf-v0.2` followed by `knowl validate`.
- **Git Compatibility**: Operators commonly version `schema.md`, `raw/`, and `wiki/` under Git, treating `.knowl/` as local state. Knowl does not perform automated Git commits or pushes.
