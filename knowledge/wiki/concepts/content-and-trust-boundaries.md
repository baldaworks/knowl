---
type: topic
title: Content and Trust Boundaries
knowl:
  id: concepts/content-and-trust-boundaries
  source_refs:
    - wiki-filesystem:knowl-docs/design.md@15d3f23357e10afc7a1f9e7ba3dda84f262521d5ae864d5fbc4ed95c822ec4e0
    - wiki-filesystem:knowl-docs/workspace.md@f3ad44d1383b2256e486b9da1e4943e6afaf6af471594db73ef3d87e4abc89f3
---
# Content and Trust Boundaries

Knowl maintains strict separation between canonical content, operational projections, and untrusted inputs. The workspace is the canonical artifact, with `wiki/` serving as a directly portable Open Knowledge Format (OKF) v0.2 bundle alongside immutable raw sources.

## Workspace Layout and Ownership

Workspace ownership is split between operator policy, raw evidence, canonical knowledge, and operational state:
- `schema.md`: Required, operator-controlled Markdown policy (`schema_version: 1`) providing untrusted guidance to the maintainer. It is not an executable schema or validation DSL. Maintainer plans read it, but edit validation rejects policy modifications. Exact byte digest is tracked with every operation; changes invalidate stale plans.
- `raw/`: Stores immutable accepted source revisions and manifests (`raw/<scope/source identity>/<version>/source` and `manifest.yaml`). The filesystem adapter safely tokenizes raw paths; logical identity is `(scope, adapter, source ID, version)`. Same identity/version with differing digests produces a conflict.
- `wiki/`: Accumulated canonical OKF v0.2 bundle (`entities/*.md`, `concepts/*.md`, `syntheses/*.md`, and `catalogs/**/index.md`). Human-readable, Git-compatible, and contains no configured-source mirror copies.
- `wiki/index.md` & `wiki/log.md`: Reserved control documents excluded from retrieval evidence. The root index declares `okf_version: "0.2"`. `wiki/log.md` is an append-only, newest-first OKF audit log maintained by the host; each committed operation records a structured JSON line (`operation_id`, `generation`, `schema_digest`, `source_refs`, `files`).
- `.knowl/`: Operational state. `.knowl/staging/` and `.knowl/recovery/` manage staging manifests, atomic commits, and interruption recovery journals. `.knowl/knowl.sqlite` holds the rebuildable default operational store.
- **Workspace Lifecycle**: `knowl init` creates the workspace skeleton, starter `schema.md`, `wiki/index.md`, and `wiki/log.md`. `knowl validate` verifies required workspace paths.

## OKF Page Contract and Provenance

Curated semantic pages must follow enforced OKF v0.2 conventions:
- **Concept Identity**: The safe bundle-relative path without `.md` defines the concept identity (`knowl.id`).
- **Metadata Fields**: `type` is required; `title`, `description`, `resource`, and `tags` follow OKF v0.2. Unknown producer fields round-trip as OKF extensions.
- **Provenance Citations**: The `knowl.source_refs` extension retains stable citations in the form `adapter:source-id@version`. Every maintained factual page must cite at least one accepted raw source. Manifests record structured `source_document` metadata (`source_id`, `document_id`, `revision`, `uri`), resolved into sorted collections during snapshots.
- **Link Integrity**: Curated pages use strict double-bracket wiki links (`[[concepts/architecture]]`) targeting confirmed bundle-relative page identities. Imported OKF concepts use standard Markdown links (`[Related](related.md)`). Unresolved internal targets are rejected; external URLs and assets are excluded from the concept graph.
- **Edit Bounds**: Maintainer edit plans may target safe semantic paths under `wiki/**/*.md`, but cannot modify `wiki/log.md`, `schema.md`, or the reserved legacy `wiki/sources/**` boundary.

## Operational Projections and Staging Recovery

SQL stores (SQLite FTS and PostgreSQL) and search state are rebuildable operational indices, not canonical content:
- **Lexical Indexing**: Projections prioritize four semantic fields in descending order: (1) Title, (2) Normalized OKF tags, (3) OKF description, (4) User-authored body. Filenames, paths, extensions, and provenance metadata are excluded from lexical ranking.
- **Atomic Commits & Recovery Journals**: Content commits write staging manifests and preimage recovery journals before atomically replacing files. Startup recovery processes journals before readiness: `prepared` journals rollback preimages, `committed` journals are finalized, and partial staging is discarded.
- **Version Control**: Workspaces are suitable for Git review, but Knowl never commits, pushes, or syncs remote repositories.

## External and Provider Trust Boundaries

- **Host & Scope**: The host controls user context and maps calls to a trusted Knowl scope. Callers cannot override the assigned scope.
- **Connectors**: Ingest connectors and source adapters cannot write directly to the workspace or SQL store. Complete scans tombstone deleted sources, while interrupted scans never authorize deletions.
- **Maintainer Provider**: Resolved via `knowl.provider` under `runtime.providers`. Operates under bounded untrusted context and structured-output constraints, without unrestricted filesystem access. Proposed plans are validated before commit.
- **Attested Computation**: OKF Attested Computation declarations are stored and exposed as inert data; they are never executed or dereferenced.

See [[concepts/architecture]] for overall architecture, [[concepts/public-contract]] for service APIs, and [[concepts/service-operations]] for operational CLI commands and maintenance workflows.
