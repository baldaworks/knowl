---
type: concept
title: Maintenance Contract and Execution
knowl:
  id: concepts/maintenance-contract-and-execution
  source_refs:
    - wiki-filesystem:knowl-docs/design.md@c9a981be23b39a137da77ad85726d8382d33306b44e801022bcdd4de4b525132
    - wiki-filesystem:knowl-docs/operations.md@ae3c7b6ee1583fd88681e5a3ee4bb1cdf2a9b007235663169cf7e59a41b60baa
---
# Maintenance Contract and Execution

Knowl coordinates wiki updates through a deterministic maintenance pipeline that pairs configured maintainer providers (see [Agent Maintainers and Provider Configuration](agent-maintainers.md)) with strict request sizing, bounded output correction, durable operation reporting, and serialized publication.

## Source Signals and Context Selection

Before selecting context for source maintenance, ingest extracts a detached `SourceSummary` (`title`, `tags`, `headings`, and `body`) from the accepted UTF-8 source without rewriting raw bytes or summarizing via a model (`SourceText` passed to the provider remains complete within read limits):

- **YAML Frontmatter and Markdown Precedence**: A source may start with exact `---` delimiter lines (LF or CRLF) around YAML frontmatter where `title` is a string and `tags` is a sequence of strings. Without a usable YAML title, extraction uses the first eligible ATX heading (`#`–`######` followed by space/tab), Setext heading (`=` or `-` underline), or first eligible nonempty prose line. Backtick/tilde fences (3+ markers, up to 3 leading spaces) and 4-column indented code are excluded. Malformed YAML, aliases, merge keys, invalid types, or exceeded bounds discard all metadata signals and fall back to Markdown (closed metadata blocks remain excluded from prose). Invalid UTF-8 fails before inference with class `source_signals` while preserving the accepted raw revision.
- **Fixed Signal and Parser Limits**:
  | Signal / Parser Limit | Fixed Ceiling |
  | --- | --- |
  | Source read (defaults) | 4 MiB and 262,144 characters per document |
  | YAML metadata | 262,144 bytes, 16,384 nodes, nesting depth 64 |
  | Title / each tag / each heading | 256 Unicode runes |
  | Tags / headings examined | First 32 entries in each list |
  | Examined raw source signals (direct index) | 65,536 bytes (64 KiB) combined before clipping |
  | All semantic signals combined | 4,096 runes and 16,384 UTF-8 bytes |
  | Lexical query | 32 distinct terms and 256 total normalized term runes |
- **Context Query Generation**: Consumes the combined budget in `title`, `tags`, `headings`, `body` order. Source ID and adapter are fallback inputs only when semantic fields yield no usable lexical terms. Note that `SourceSummary` contains slice fields (`Tags`, `Headings`) and is non-comparable with `==` in Go. See [Retrieval and Hybrid Search Architecture](retrieval-and-search.md).

## Source Maintenance Contract v6 (`source-maintenance-v6`)

`MaintenanceInput.contract_version` is `source-maintenance-v6`:

- **Factual Pages (`pages`)**: Selected ordinary factual snapshots (default `ReadLimits.Pages` is **20**; controls and duplicates do not consume this allowance). The shared wire encoder sends complete factual `Content` once and omits `Body`. Editable pages are never excerpted. Factual edits to existing pages must target an included full snapshot and copy its exact `expected_digest`; omitted existing pages cannot be edited even if their digest is known.
- **Catalog Graph (`catalogs`) and `CatalogLimits`**: Complete bounded navigation graph sent as `HierarchyCatalog` nodes (`path`, `digest`, `title`, sorted `children`), with `wiki/index.md` first. Full catalog Markdown stays in the application. `pkg/knowl/types.CatalogLimits` defaults (configured in Go via `knowl.Config.IngestOptions.CatalogLimits`, all 7 fields positive or all zero):
  | Go Field / JSON Field | Default | Measures |
  | --- | --- | --- |
  | `MaxCatalogs` / `max_catalogs` | 1,024 | All catalogs, including root |
  | `MaxEdges` / `max_edges` | 16,384 | Unique internal child destinations summed across catalogs |
  | `MaxDepth` / `max_depth` | 16 | Longest catalog chain, including root |
  | `MaxPathBytes` / `max_path_bytes` | 2,048 bytes | Canonical catalog and child paths |
  | `MaxCatalogBytes` / `max_catalog_bytes` | 262,144 bytes | Each original or rendered catalog |
  | `MaxSnapshotBytes` / `max_snapshot_bytes` | 4,194,304 bytes | All original or rendered catalog Markdown combined |
  | `MaxInputBytes` / `max_input_bytes` | 4,194,304 bytes | Serialized JSON catalog graph |
- **Additive Navigation (`catalog_additions`) and Plan Limits**:
  - Existing catalogs require their original `expected_digest` and omit `title`; new catalogs omit `expected_digest` (or pass empty) and require a nonempty single-line `title` and `children`.
  - Generated catalog files and factual edits share plan defaults of **32 files** and **256 KiB per file**.
  - Arbitrary `index.md` `FileEdit` mutations are rejected. Every new catalog and edited factual page must be reachable from `wiki/index.md` without cycles.
- **Safe-Subset Commit and Link Diagnostics**: Committed operations may record bounded `diagnostics` (`code`, `path`, optional `target`). An unresolved wiki link is preserved only if the same normalized target existed in the accepted raw source (`link.original_unresolved`). If one generated document has invalid provenance, Knowl withholds that document and its dependent catalog closure and may commit the remaining safe subset (failing with `plan.empty_safe_subset` if none remains).

### Complete Request Budget and Policy Fingerprinting

- **Request Ceiling**: Default and maximum local ceiling is **4,194,304 UTF-8 bytes (4 MiB)** (`knowl.Config.IngestOptions.InputLimits` via `types.MaintenanceInputLimits{MaxRequestBytes: ...}`), covering the JSON envelope, source, base64 schema, catalog graph, selected pages, instructions, schemas, and wrapper framing.
- **Fitting and `MaintenanceRequestSizer`**: The built-in runtime reserves **128 bytes** for correction feedback. If indispensable input (`source_text`, `schema`, `catalogs`) plus the reserve exceeds the cap, the operation fails permanently with class `input_budget` and reason `required_input_limit` before factual reads or inference. Otherwise, ordinary candidates are fitted in priority order; a large omitted page does not block smaller subsequent pages. `app.MaintenanceRequestSizer` declares `MaxBytes`, `ReservedBytes`, and `FormatVersion` (up to 256 UTF-8 bytes); unexpected wrapper overflow fails with `provider_input_limit`. Transient `IngestResult.Budget` and durable `details.context.budget` record fitting metrics.
- **Policy Generation and Upgrading Pending Operations**: Fingerprints contract version, schema digest, effective request cap/format identity, read/plan/catalog limits, output correction settings, and embedding space/chunk/fusion/candidate/capacity/failure policy. Unplanned queued work from an older generation fails before inference with class `maintenance_policy` and reason `maintenance_policy_mismatch` (recover via `./knowl source retry <id> --failure-class maintenance_policy` or by resubmitting public ingest with the same origin and idempotency key). Already validated v1–v5 concrete stages and authenticated hierarchy stages resume without new inference.

## Bounded Output Correction and Scheduler Retries

- **Configuration (`knowl.output.max_corrections`)**: Accepts `0` or `1` (defaults to `1`; Go: `Config.Output` / `types.OutputSettings`). Governs both source maintenance and explicit hierarchy planning.
- **Correction Loop**: Requests at most one fresh replacement on malformed JSON, schema/branch rejection, or application-plan rejection using only a single safe feedback code (`structured_output_invalid`, `source_plan_invalid`, or `hierarchy_plan_invalid`) over the original input.
- **Hard Turn Bounds**: All turns in a work attempt share **1,048,576 output bytes (1 MiB)** of collector text (excluding thought text) and **5 minutes** total planning time. Exhaustion, output overflow, and internal deadline failures are permanent (`provider_output_exhausted`, `provider_output_limit`, `provider_output_deadline`).
- **Scheduler Retries**: Transient provider build, transport, and execution failures are retried by the scheduler up to **3 total work attempts**, waiting at least **30 seconds** initially and growing exponentially with bounded jitter up to **5 minutes**.

## Durable Operation Details (`GET /v1/operations/{id}` / `knowl_operation`)

Operation `details` stay below **96 KiB** (context capped at **32 KiB / 100 entries**, identifiers at **2,048 bytes**, retrieval at **2 KiB**, warnings at **64 entries / 32 KiB**, correction at **1 KiB**):

- **`context` (Migration 17)**: `work_attempt`, `outcome` (`assembled`, `selection_failed`, `assembly_failed`), `candidate_count`, `catalog_count`, `pages` (with selection reason `lexical`/`vector`/`hybrid`/`neighbor`/`recent`/`unknown` and disposition `included`/`budget_omitted`/`pending`), `entries_omitted`, `budget` (`max_bytes`, `used_bytes`, `included_count`, `omitted_count`), and `vector_projection` (`not_checked`, `ready`, `invalid`).
- **`retrieval` and `retrieval_attempt`**: Requested/effective modes, safe reason, counts, and model-space fingerprint.
- **`correction` (Migration 18)**: `work_attempt`, `max_corrections`, `max_output_bytes`, `deadline_nanos`, `outcome` (`accepted`, `exhausted`, `output_limit`, `deadline`, `canceled`, `provider_failed`, `unavailable`), and optional `turns`, `corrections`, `output_bytes`, `validation_code`. Written with a **5-second** timeout.
- **`plan`, `warnings`, and `execution`**: Staged SHA-256 `digest` and `file_count`, warning codes, and counters (`work_attempt`, `retry_attempt`, `manual_retry_count`, `apply_attempt`, `ready_at`).

## Maintenance Workers, Publication Gate, and Failure Recovery

- **Workers (`knowl.workers`)**: Supports integer `1` (default) or `2` (0, strings, fractions, or >2 fail preflight). Programmatic singleton `Options.Maintainer` requires `1`; `RuntimeFactory` builds a fresh agent per owner. Direct hierarchy and model-lint calls share this capacity under a 5-minute admission+execution bound.
- **Publication Gate and Conflicts**: Inference runs outside the canonical write lock, while a shared publication gate serializes commits and projections. Stale log/catalog/page preconditions fail permanently with class `canonical_conflict` and reason `precondition_failed` without automatic replanning or output correction.
- **Recovering Failed Source Maintenance**: On sync, unchanged documents with an older committed operation or older terminal `source` failure receive one new current-generation operation, whereas `staging`, `provider`, and unknown failures require explicit retry (`maintenance_trigger`: `revision`/`policy`/`unchanged`; `maintenance_outcome`: `queued`/`replayed`/`converged`/`manual_gate`). Preview and requeue specific failure classes (recovering `provider` failures first) via:
  ```bash
  ./knowl source retry <source_id> --failure-class provider --dry-run
  ./knowl source retry <source_id> --failure-class provider
  ```
  The structured JSON response reports `source_id`, `dry_run`, `matched`, `requeued`, `rejected`, `operation_ids` (up to 100), and `truncated`. Requests fail atomically if any selected candidate is committed, stale, cross-scope, not source maintenance, or actively leased.
