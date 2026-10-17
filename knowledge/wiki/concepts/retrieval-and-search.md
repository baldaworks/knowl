---
type: concept
title: Retrieval and Hybrid Search Architecture
knowl:
  id: concepts/retrieval-and-search
  source_refs:
    - wiki-filesystem:knowl-docs/design.md@c9a981be23b39a137da77ad85726d8382d33306b44e801022bcdd4de4b525132
    - wiki-filesystem:knowl-docs/operations.md@ae3c7b6ee1583fd88681e5a3ee4bb1cdf2a9b007235663169cf7e59a41b60baa
    - wiki-filesystem:knowl-docs/postgres.md@70888829c1127d091a9c411bbe062a67d3e9da37094408df577821faf8c5c656
    - wiki-filesystem:knowl-docs/search.md@0288624ba804af0db6783a6c0b46017e19a3b169bf36ee2264d8f8150e3befc2
---
# Retrieval and Hybrid Search Architecture

Knowl provides deterministic lexical search by default and optional hybrid retrieval combining lexical search with dense vector similarity. SQL and search indexes are operational, rebuildable projections derived from the canonical `wiki/` workspace across project-local CLI, MCP/HTTP service, and embedded Go deployments. Embedding models match related wording without generating final answers or replacing the wiki maintainer (see [Knowl Overview and Operating Modes](knowl-overview.md), [Agent Maintainers and Provider Configuration](agent-maintainers.md), and [Maintenance Contract and Execution](maintenance-contract-and-execution.md)).

## Lexical Projection and Semantic Field Priority

The lexical index projects four semantic fields from ordinary wiki pages in descending priority order:

1. **Title**
2. **Normalized OKF tags**
3. **OKF description**
4. **User-authored body**

Paths, filenames, OKF extensions (`knowl.*`), source references, source documents, and other provenance or transport metadata are excluded from lexical ranking and evidence snippets. Reserved control files (`wiki/index.md` and any `index.md` or `log.md`) are also excluded from retrieval evidence.

SQLite FTS and PostgreSQL text search share literal token matching, field priority, filters, strict-then-relaxed (AND then OR) retrieval, and deterministic path tie-breaking, though their native scoring formulas and PostgreSQL position limits may order non-tied results differently.

## Generic Literal Retrieval, Token Encoding, and Direct-Index Bounds

- **Word Scanner and Normalization**: A single scanner extracts Unicode letter/number words with attached combining marks. Each complete word is normalized via `NFC -> strings.ToLower -> NFC`. Knowl performs no language detection, full case folding, accent stripping, stemming, or transliteration. Consequently, ordinary English/Russian casing and composed/decomposed accents match (`CAFÉ` matches `Café`), while unaccented `cafe` remains distinct. Stop/question words (such as `what` and `why`) are indexed and queried like any other word.
- **Base32 `k`-Prefixed Encoding**: Normalized words are encoded as lowercase unpadded base32 of their UTF-8 bytes prefixed with `k`. This private reversible representation avoids native SQLite/PostgreSQL tokenizer discrepancies without hashes or extra persistent text columns:
  - **SQLite**: Writes encoded tokens only into existing FTS semantic columns.
  - **PostgreSQL**: Reuses its existing GIN index and builds its A/B/C/D-weighted `search_vector` from transient encoded fields in the same transaction as original page values.
- **Snippets and Evidence**: Generated from original semantic fields and rune spans, preserving exact spelling, case, and combining marks (native DB snippet/headline functions are not used). Tag-only matches retain their `tag:` label.
- **Fixed Direct-Index Bounds**:
  | Input / Result | Supported Bound |
  | --- | --- |
  | Query passed to the index | Valid UTF-8, at most 65,536 raw bytes |
  | Query terms | 32 distinct terms, 256 normalized runes combined |
  | Examined raw source signals | Valid UTF-8, 65,536 bytes combined before clipping; first 32 tags and headings |
  | Original semantic field | Valid UTF-8, 4,194,304 bytes (4 MiB) per title/tags/description/body field |
  | Derived page token streams | 524,288 bytes (512 KiB) across all four fields; 8,192 distinct supported words |
  | Queryable word | At most 256 normalized runes (longer words are excluded whole from the index) |
  | Source filter | At most 256 raw entries and 16 distinct validated source IDs, sorted/deduplicated |
  | Direct-index snippet | Nonpositive character limit defaults to 4,096 runes; positive limits clamp at 262,144 |
  Invalid queries, source summaries, or projections return stable typed errors (`ErrInvalidProjection` aliases the shared projection sentinel). If a page exceeds derived token limits, rebuild fails atomically while preserving the previous scoped index and readiness.
- **Migration 15 (`00015_generic_lexical`)**: Clears old FTS tokens / PostgreSQL vectors and projection readiness while preserving original pages, links, provenance, operations, and files; startup rebuilds the derived projection before serving readiness (`Down` invalidates readiness, restores PostgreSQL's earlier generated A/B/C/D vector, and recreates empty SQLite FTS).

## Optional Hybrid Retrieval (`knowl.embeddings`)

Embeddings are disabled by default (creating no embedding client, reading no credential, and making no model/readiness request). Opt in by configuring an OpenAI-compatible float embedding API or the local TEI server:

```yaml
knowl:
  embeddings:
    enabled: true
    endpoint: http://tei:80/v1/embeddings
    model: intfloat/multilingual-e5-base
    revision: d128750597153bb5987e10b1c3493a34e5a4502a
    dimensions: 768
    query_prefix: 'query: '
    passage_prefix: 'passage: '
    failure_policy: lexical
```

- **Endpoint, Credentials, and Bounds**:
  - `endpoint`: Full URL (at most **2,048 UTF-8 bytes**) for one OpenAI-compatible float embedding API. Supports HTTPS with normal TLS or private HTTP; userinfo, fragments, redirects, and hidden inference retries are rejected/disabled.
  - `api_key_env`: Optional environment variable name (e.g., `YOUR_EMBEDDING_KEY`) holding a bearer credential; if configured, a missing variable fails startup.
  - `model`, `revision`, `query_prefix`, `passage_prefix`: Bounded at **256 UTF-8 bytes** each (`model` and `revision` are required when enabled; prefixes may be empty if the model requires none).
  - `dimensions`: Required integer in **1–4,096**.
  - **Weight Alignment**: Knowl verifies the response `model` name and vector dimension, while operators must keep the declared immutable `revision` aligned with the served weights and update `revision` plus rebuild whenever weights change.

### Chunking, Input Preparation, and Client Inference Bounds

- Semantic inputs preserve original case and use NFC with normalized line endings; private lexical tokens, file paths, and provenance are excluded.
- **Page chunking**: Follows Markdown/OKF heading sections and their heading ancestry up to at most **384 runes** per input (accompanying title and semantic metadata), splitting long sections at block boundaries and oversized blocks into bounded pieces. Every eligible page is chunked to the end with zero omissions on a ready projection.
- **Queries and source signals**: Use at most **four 384-rune windows** with a **64-rune overlap** and report any omitted chunk/rune coverage in Go reports. Model prefixes are applied once. Rune limits are not tokenizer limits; the embedding server must reject token overflow (`input_limit` on pinned TEI) rather than silently truncating.
- **Embedding API Request / Response Bounds**:
  | Aspect | Fixed Bound |
  | --- | --- |
  | Batch request size | At most 16 inputs, 2,048 UTF-8 bytes per input (including prefix), 64 KiB serialized body |
  | Batch response size | At most 1 MiB; complete unique indices, exact model/dimensions, finite nonzero normalized vectors |
  | Client inference concurrency & timeout | At most 4 concurrent requests; 10-second per-request bound (or earlier caller deadline), no hidden retry |
  | Projection rebuild deadline | 15-minute rebuild bound (or earlier caller deadline) |

### Vector Storage, Trust Boundary, Rebuild, and Migrations

- **Native Database Cosine**: Exhaustive page maxima use exact native database true cosine similarity across all query windows and chunks—bundled CGO-free `sqlite-vec` on SQLite (storing portable little-endian `float32` BLOBs even with `CGO_ENABLED=0`, needing no extension install, shared-library load path, or external model process for SQL math) or optional operator-owned `pgvector` on PostgreSQL (using the extension's unbounded `vector` type with a dimension constraint under a scoped advisory lock). Knowl never installs `pgvector` itself or falls back to the legacy Go near-unit dot-product ranking path. No in-process embedding model, ANN index, or chunk shortlist is used.
- **Write-Time Validation vs. Read-Time Trust Refinement**:
  - Knowl owns database writes: publication, maintenance reuse, and recovery fully validate coverage, model/dimensions, counts, budgets, and vectors before atomic ready publication, and page edits invalidate incompatible projections.
  - Ordinary reads, context selection, startup, and lint admit existing scalar readiness, snapshot, and active model state in one consistent read transaction without transferring stored vectors into Go or repeating full-scope coverage/vector integrity audits (detecting arbitrary out-of-band SQL corruption in nonselected rows on every read is outside this trust boundary). Search still validates the current page digest, chunk ordinal, and content hash whenever it consumes winning chunk evidence. This trust refinement reuses existing validated `native_format` readiness without a new migration, cache, setting, or rebuild.
- **Incremental Reuse vs. Explicit Rebuild**:
  - After source sync or canonical edits, a projection refresh reuses a prior vector only when its model space, complete prior state, and prepared input match (unchanged snapshots or source-only revisions need no new page embeddings; changed sections recompute).
  - An explicit rebuild (`knowl.RebuildProjection(ctx, config, snapshot)` or startup retry after repairing a degraded service) replaces lexical state and recomputes every page vector in batches of at most 16 outside SQL locks and transactions, publishing only if the canonical snapshot still matches. Read queries never trigger a rebuild or model download.
- **Historical and Native Migrations**: Migration `16` added disposable vector state/reports, migration `21` added page coverage, and Goose SQL migration `26` added the `native_format` marker and invalidated derived dense state on both SQLite and PostgreSQL.
- **Latency and Capacity Characteristics**: Exhaustive cosine scoring remains database work proportional to chunk population and query windows (`ScannedChunks` reports the admitted published full-scope chunk count before source filtering). Zero stored-vector transfer and lower Go allocation volume do not guarantee faster queries; paired measurements record mixed latency results (including PostgreSQL regressions with source filtering and visible original v1.3 regressions), and isolated 100,000-chunk kernels omit production integrity/caps.

### PostgreSQL Storage, Two Goose Migration Streams, and `pgvector` Operations

PostgreSQL lexical search requires no `pgvector` extension. Configure PostgreSQL via `knowl.storage` (using `postgres.dsn`; there is no `dsn_env` key, and secrets/overrides belong in `.config/knowl/*.local.yaml` or environment expansion):

```yaml
knowl:
  storage:
    type: postgres
    postgres:
      dsn: 'postgres://KNOWL_RUNTIME@DB_HOST/KNOWL_DB?sslmode=verify-full'
```

Opening a PostgreSQL store uses two standard Goose migration streams (tested locally with PostgreSQL `16.14` and `pgvector` `0.8.2`; normal Goose-on-Open and historical Go migration `19` remain in use):

| Stream | Sources | Standard History Table | Current Native Transition |
| --- | --- | --- | --- |
| **Base** (always considered on Open) | `pkg/knowl/store/postgres/migrations` + historical Go migration `19` | `goose_db_version` | SQL migration `26` adds `native_format` and invalidates derived dense state; remains extension-free |
| **Optional** (enabled dense + verified `pgvector` only) | `pkg/knowl/store/postgres/pgvector_migrations` | `goose_pgvector_db_version` | SQL migration `1` replaces derived `BYTEA` vectors with extension-owned unbounded `vector` and a dimension constraint |

- **`Up` Ordering and Extension Admission**: `Up` runs **base before optional**. If `pgvector` is unavailable, optional SQL stays pending (never falsely recorded as applied), lexical-only mode remains usable, and `goose_pgvector_db_version` is not created. Dense admission resolves `pgvector`'s owned type, functions, and operator from system catalogs (supporting a trusted nondefault extension schema while rejecting untrusted same-named search-path objects). Querying or rebuilding does not install the extension, repair migration history, or clear a cached `unavailable` admission; reopen with embeddings enabled after provisioning.
- **Maintenance-Window Provisioning and Pre-Application**:
  1. Stop all writers/hosts (including old binaries) and back up canonical/raw data and the database (including both Goose history tables).
  2. Initialize or upgrade the base stream through migration `26` using a compatible Knowl binary (because standalone Goose SQL-directory runs do not include historical Go migration `19`).
  3. Provision the extension as a privileged PostgreSQL operator (`CREATE EXTENSION vector;`—never place extension creation inside Knowl's base migration directory).
  4. For the optional SQL-only stream, either reopen with migration authority or pre-apply using a Goose CLI compatible with `v3.27.3` (passing `GOOSE_DBSTRING` in the environment with shell tracing disabled):
     ```sh
     GOOSE_DRIVER=postgres goose -env none \
       -dir pkg/knowl/store/postgres/pgvector_migrations \
       -table goose_pgvector_db_version status
     GOOSE_DRIVER=postgres goose -env none \
       -dir pkg/knowl/store/postgres/pgvector_migrations \
       -table goose_pgvector_db_version up
     ```
  5. **Runtime Role Permissions**: A non-migrating runtime role using a pre-applied schema requires ordinary Knowl table/sequence access, extension schema usage/function execution, and `SELECT` on **both** `goose_db_version` and `goose_pgvector_db_version` (grant `SELECT` on `goose_pgvector_db_version` only after pre-applying; never create an empty history table as a permission workaround). Reopen, rebuild the projection, and verify semantic retrieval reports before resuming writers (`/readyz` alone does not prove dense readiness).
- **Corrupt History vs. Transactional Failure**: A normal transactional failure rolls back cleanly so a valid version `0` history can retry on reopen. By contrast, an **existing but empty** `goose_pgvector_db_version` table (or unknown/duplicate/inconsistent history) is treated as corrupt history: Knowl fails closed with a redacted typed `unavailable` reason and never auto-bootstraps or repairs it. Recover outside Knowl from a known-good backup (never fabricate version `0`, drop/recreate history, or use `-no-versioning`).
- **Rollback Order (`Down`)**: Stop writers and back up first. Roll the optional stream `Down` **before** base SQL migration `26` `Down` (base migration `26` refuses rollback while the native `vector` column type remains):
  ```sh
  GOOSE_DRIVER=postgres goose -env none \
    -dir pkg/knowl/store/postgres/pgvector_migrations \
    -table goose_pgvector_db_version down
  ```
  Optional `Down` invalidates derived dense state and reverts the column to `BYTEA`. Use a matching Knowl binary/provider for base rollback (especially across Go migration `19`), pair the rolled-back schema with the compatible binary, and rebuild before resuming. Do not reopen the new dense-enabled binary mid-rollback.

### Reciprocal Rank Fusion (RRF) and Candidate Selection

- SQL aggregates all eligible native chunk scores to page winners before limiting candidates. Equal native scores break ties by lowest chunk ordinal and then bytewise page ID; close unequal scores retain native database order without a production epsilon (which can differ from the legacy Go dot product or across SQLite/PostgreSQL).
- Each retrieval channel (lexical and dense) retains `min(100, max(20, 4*k))` candidates.
- **Equal-weight RRF**: Uses 1-based ranks and constant `60`, deduplicating pages per channel and breaking ties by page ID. Fused relevance seeds feed the existing neighbor, root, and recent-page context policy.
- **Snippet Selection**: Pages matched by both channels retain their lexical snippet. Dense-only matches use original text from the winning chunk, verified against the indexed page digest, chunk ordinal, and chunk hash.

### Capacity Bounds, Model-Space Fingerprint, Retrieval Reporting, and Failure Policy

- **Projection Capacity**: Bounded per scope to **8,192 chunks**, **1 MiB** of page coverage metadata, and **64 MiB** total logical `float32` vector + metadata state (excluding extra PostgreSQL native-storage headers).
- **Model-Space Fingerprint**: Binds model name, immutable revision, dimensions, prefixes, structured page input version, preprocessing, and normalization (`source-maintenance-v6` also binds fusion, candidate/capacity bounds, and failure policy; endpoint addresses, credentials, and CPU settings are excluded). Changing the model space or structured page input version changes maintenance identity and triggers a one-time dense rebuild on startup.
- **Retrieval Status Reporting**:
  - Effective modes are `lexical`, `hybrid`, `degraded`, and `failed`.
  - HTTP/MCP responses expose only `effective` and optional `reason` (e.g., `{"retrieval":{"effective":"degraded","reason":"unavailable"}}`).
  - Go `QueryResult.Retrieval`, `IngestResult.Retrieval`, and durable `Operation.Retrieval` also record requested mode, model-space prefix, candidate/scanned-chunk counts, and omitted coverage (with `Operation.RetrievalAttempt` identifying the originating work attempt; terminal/legacy replay never invents or overwrites historical reports, and reports omit text, endpoint URLs, credentials, and upstream error bodies).
- **Failure Policy (`failure_policy`)**:
  - `lexical`: Saves degraded state and returns explicit `degraded` lexical results with a safe reason when a classified embedding failure occurs (note that `/readyz` can succeed while semantic retrieval is `degraded`).
  - `strict`: Saves degraded state and returns a typed failure when dense retrieval is missing, unavailable, or incompatible (if a strict projection error occurs after a canonical commit, repair the derived projection while preserving committed facts and raw evidence).
  - Invalid caller input and caller cancellation never trigger fallback. Disabling embeddings restores default lexical behavior and ignores derived vectors.

## Reference Local TEI Embedding Server (`deploy/sidecar/embeddings.compose.yaml`)

For sidecar Compose deployments, opt in with the checked-in `deploy/sidecar/embeddings.yaml` and `deploy/sidecar/embeddings.compose.yaml` overlay (after setting the base service environment variables; see [Service Configuration, HTTP/MCP Transports, and Operator API](service-configuration-and-transports.md)):

```bash
docker compose -f deploy/sidecar/compose.yaml \
  -f deploy/sidecar/embeddings.compose.yaml up --build -d
docker compose -f deploy/sidecar/compose.yaml \
  -f deploy/sidecar/embeddings.compose.yaml ps tei
```

- **Complete Replacement Config**: The overlay mounts `deploy/sidecar/embeddings.yaml` as a complete replacement configuration (preserving the base profile's OpenAI maintainer, `engineering` source, storage, and operator token rather than merging individual keys; carry any custom base settings into your hybrid config before switching).
- **Pinned TEI Service Contract**: Runs the pinned `linux/amd64` CPU TEI `1.9.0` image (pinned by digest in the overlay) with `intfloat/multilingual-e5-base` revision `d128750597153bb5987e10b1c3493a34e5a4502a` (`768` normalized `float32` dimensions, mean pooling, required `query: ` / `passage: ` prefixes, no second server prompt, and server auto-truncation disabled) on the private Compose network (`http://tei:80/v1/embeddings`) with no published host port and no GPU.
- **Model Cache and Resource Profile**: Downloads model weights on first start into the `tei-model-cache` named volume (persist for offline restarts; `docker compose down` preserves named volumes). Allocates **2 CPU cores**, **4 GiB RAM**, and a **10-minute** cold-start health allowance (recorded reference measurements: `194.5s` cold readiness, `6.6s` cached restart, `1,075 MiB` model cache, and `~1.91 GiB` idle memory).
- **Startup and Project-Local Use**: Under `failure_policy: lexical`, Knowl can start during TEI cold loading and serve `degraded` lexical fallback until restarted once TEI is healthy; under `failure_policy: strict`, wait for TEI health before starting Knowl. For project-local CLI use, run TEI separately with its port bound only to host loopback (`127.0.0.1`) and point `knowl.embeddings.endpoint` to that loopback `/v1/embeddings` URL.
