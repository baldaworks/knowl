---
type: topic
title: Content and Trust Boundaries
knowl:
  id: concepts/content-and-trust-boundaries
  source_refs:
    - wiki-filesystem:knowl-docs/design.md@a3099f6ef8a7094ced149ddd0e73f9b95c5a9a44c8d8007c6ec3f4b764aa4f41
    - wiki-filesystem:knowl-docs/workspace.md@df6e46c335deb68c9ecd6769ffe5a36d3c3d953716ee37bee83c8fdfac44aa45
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

## Browser Inspection and Provenance Safety

- **Web UI Provenance Inspection**: The optional web UI reads current canonical `wiki/` pages and immutable accepted revisions from `raw/` without upstream fetching. Missing, oversize, or non-text revisions report failures rather than substituting newer upstream documents.
- **URI Sanitization for Original Sources**: "Open original" links appear only for provenance recording safe HTTP/HTTPS URIs with user credentials, query strings, and fragments removed. Filesystem paths, unsafe schemes, and credentials are never exposed as clickable originals, and navigation never forwards the operator bearer token.
- **Whole-Page Attribution**: Source citations support the page as a whole rather than sentence-level claims. Browser reads reflect current canonical state; accepted raw revisions retain immutable historical identity independently.

## Export for Publication

- **Standalone OKF Bundle**: `knowl export okf --output ./public` copies the canonical `wiki/` directory without `schema.md`, `raw/`, `.knowl/`, or database files.
- **LLM Navigation**: `knowl export llms-txt --output ./public/llms.txt` renders bundle-relative navigation beside exported pages following the llms.txt v2 proposal (`--base-url`, `--title`, `--summary`).

## Explicit Hierarchy Reconciliation

- **Trigger & Scope**: Reorganizes catalog structures only on explicit `knowl hierarchy reconcile`, owning only `wiki/index.md` and generated `wiki/catalogs/**/index.md`.
- **Subject-First Planner**: Operates under durable planner identity `hierarchy-v3` with subject domains as primary navigation axes; pages receive primary placements, and secondary memberships are added sparingly.
- **Conservative Bounds**: Capped at 1,024 ordinary pages, 1,024 catalogs, 16,384 edges, max depth 16, 4 MiB planner input, 4,096 excerpt chars/page, 1 MiB output, 1,024 managed edits, 256 KiB per generated catalog, and 1 MiB stage manifest.

## Source Maintenance Contract v6

- **Wire Contract and Envelopes**: `MaintenanceInput.contract_version` is `source-maintenance-v6`. The wire encoder sends complete factual `Content` once and omits `Body`. Default and supported local request ceiling is 4,194,304 bytes (4 MiB).
- **Indispensable Inputs**: Complete source text, schema, and bounded catalog graph are required; if these alone exceed the request cap, assembly fails immediately with `input_budget/required_input_limit`.
- **Candidate Selection & Excerpts**: Ordinary pages are read in priority order, one full page at a time. Omitted pages do not prevent later smaller pages from fitting. Editable pages are never excerpted; existing factual edits must target an included full snapshot and copy its exact digest.
- **Additive Catalog Additions**: Source plans submit navigation memberships through `catalog_additions`. Existing catalogs require their exact input digest and omit titles; new catalogs require empty expected digests, single-line titles, and child links. Arbitrary model `index.md` edits are rejected.
- **Maintainer Sizing**: `MaintenanceRequestSizer` allows custom maintainers to declare pure capacity, format identity, and exact request size.

## Bounded Output Correction

- **Correction Limits**: `knowl.output.max_corrections` defaults to 1 replacement (accepts 0 or 1). Shared runtime loop validates structured output, schema adherence, and application rules.
- **Budget and Feedback**: All turns share a 1 MiB raw collector-text bound and 5-minute context. Rejections return safe, typed feedback codes over unchanged original inputs with a 128-byte reservation. Output limits, timeouts, transport failures, and canonical conflicts do not trigger replanning.
- **Correction Reports**: Content-free correction reports finalize per work attempt before staging or upon failure (5-second durable store timeout). Reports record turns, corrections, prefix bytes, limits, outcomes, and last validation codes.

## Generic Literal Retrieval

- **Scanning and Normalization**: Scans Unicode letter/number words with combining marks using NFC, `strings.ToLower`, and NFC. Matches case-insensitively while retaining accents (e.g. `CAFÉ` matches `Café`) without language detection or stemming.
- **Base32 Encoded Tokens**: Normalized words encode as lowercase unpadded base32 of UTF-8 bytes prefixed with `k`. Tokens populate SQLite FTS columns and PostgreSQL `search_vector` without persistent extra text fields.
- **Evidence Snippets**: Snippets preserve original spelling, case, and combining marks from original rune spans. Tag-only evidence retains the `tag:` label.
- **Projection Invalidation**: Migration 15 upgrades invalidate readiness and clear incompatible tokens, restoring readiness upon complete rebuild within the transaction.

## Source Signals and Context Selection

- **Signal Extraction**: Ingest extracts detached `SourceSummary` (title, tags, headings, body) before selecting context. YAML `title` and `tags` take precedence, followed by first ATX/Setext headings or prose lines (fenced/indented code excluded).
- **Clipping Policy**: Title, tags, headings, and body share a budget of 4,096 runes / 16,384 UTF-8 bytes. Titles and list entries are capped at 256 runes (up to 32 entries examined).
- **Lexical Query Formulation**: SQLite and PostgreSQL construct lexical queries in priority order, capped at 32 terms and 256 normalized runes, with 64 KiB combined raw signal inspection caps.

## Optional Hybrid Retrieval

- **Embedding Configuration**: `knowl.embeddings` configures an OpenAI-compatible endpoint independently of the maintainer provider.
- **Chunking and Ranking**: Fixed 384-rune chunks with 64 overlap (up to 16 chunks per API call, 4 chunks for queries/signals). Equal-weight reciprocal rank fusion (RRF) with constant 60 merges dense and lexical candidates, keeping `min(100, max(20, 4*k))` candidates with ties broken by page ID.
- **Publication and Limits**: Dense projections publish only if canonical snapshots match, capped at 8,192 chunks, 1 MiB coverage metadata, and 64 MiB total per scope. `failure_policy: lexical` enables degraded lexical fallback, while `strict` returns typed failures.

## Operational Projections and Staging Recovery

SQL stores (SQLite FTS and PostgreSQL) and search state are rebuildable operational indices, not canonical content:
- **Lexical Indexing**: Projections prioritize four semantic fields in descending order: (1) Title, (2) Normalized OKF tags, (3) OKF description, (4) User-authored body. Filenames, paths, extensions, and provenance metadata are excluded from lexical ranking.
- **Atomic Commits & Recovery Journals**: Content commits write staging manifests and preimage recovery journals before atomically replacing files. Startup recovery processes journals before readiness: `prepared` journals rollback preimages, `committed` journals are finalized, and partial staging is discarded.
- **Durable Operation Details**: Attempt snapshots record context, fitting dispositions, and projection checks. Migration 17 adds nullable `context_report` and `plan_file_count`; migration 18 adds nullable `correction_report`. HTTP and MCP allowlists cap serialized details below 96 KiB (context bounded to 32 KiB and 100 entries).
- **Git Source Synchronization**: Configured remote Git sources are fetched read-only, with each complete scan pinned to an immutable commit. Accepted source revisions are preserved as raw evidence; the remote is never modified.
- **Workspace Version Control**: Workspaces are suitable for Git review. Knowl does not commit or push the generated workspace to a remote repository; operators own its version control and publication.
- **Explicit Migration**: Upgrading legacy workspaces runs `knowl migrate okf-v0.2` and `knowl validate`, creating audit archives before marker commits.

## External and Provider Trust Boundaries

- **Host & Scope**: The host controls user context and maps calls to a trusted Knowl scope. Callers cannot override the assigned scope.
- **Connectors**: Ingest connectors and source adapters cannot write directly to the workspace or SQL store. Complete scans tombstone deleted sources, while interrupted scans never authorize deletions.
- **Maintainer Provider**: Resolved via `knowl.provider` under `runtime.providers`. Operates under bounded untrusted context and structured-output constraints, without unrestricted filesystem access. Proposed plans are validated before commit.
- **Attested Computation**: OKF Attested Computation declarations are stored and exposed as inert data; they are never executed or dereferenced.

See [[concepts/architecture]] for overall architecture, [[concepts/public-contract]] for service APIs, and [[concepts/service-operations]] for operational CLI commands and maintenance workflows.
