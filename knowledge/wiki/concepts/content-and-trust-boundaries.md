---
type: topic
title: Content and Trust Boundaries
knowl:
  id: concepts/content-and-trust-boundaries
  source_refs:
    - wiki-filesystem:knowl-docs/design.md@c9a981be23b39a137da77ad85726d8382d33306b44e801022bcdd4de4b525132
    - wiki-filesystem:knowl-docs/postgres.md@70888829c1127d091a9c411bbe062a67d3e9da37094408df577821faf8c5c656
    - wiki-filesystem:knowl-docs/search.md@0288624ba804af0db6783a6c0b46017e19a3b169bf36ee2264d8f8150e3befc2
    - wiki-filesystem:knowl-docs/workspace.md@5d9b2a57995555dce9b6c8f4b5d2dff831436705c911eb2285163420317bd746
---
# Content and Trust Boundaries

Knowl separates canonical content, rebuildable operational projections, and untrusted inputs. The filesystem workspace owns canonical knowledge: `wiki/` is a directly portable Open Knowledge Format (OKF) v0.2 bundle alongside immutable accepted raw sources.

## Workspace Layout and Ownership

- `schema.md`: Required, operator-controlled Markdown policy (`schema_version: 1`) supplied as untrusted guidance rather than an executable schema or validation DSL. Maintainer plans cannot modify it. Its exact byte digest is tracked with operations; changes invalidate stale plans.
- `raw/`: Immutable accepted source revisions and manifests (`raw/<scope/source identity>/<version>/source` and `manifest.yaml`). The filesystem adapter safely tokenizes raw paths, so physical directory names need not equal source IDs. An accepted source's `manifest_ref` identifies its stored manifest. Logical identity is `(scope, adapter, source ID, version)`; replaying the same digest returns the existing record, while differing digests for the same identity/version conflict.
- `wiki/`: Canonical semantic pages under `entities/`, `concepts/`, and `syntheses/`, with navigation under `catalogs/**/index.md`. It is human-readable and Git-compatible, without configured-source mirrors.
- `wiki/index.md` and reserved `index.md`/`log.md` documents: Control content excluded from retrieval evidence. The root declares `okf_version: "0.2"`. The host maintains `wiki/log.md` as an append-only, newest-first audit log, including structured operation records with `operation_id`, `generation`, `schema_digest`, `source_refs`, and `files`.
- `.knowl/`: Operational state, including `.knowl/staging/`, `.knowl/recovery/`, and the default `.knowl/knowl.sqlite` database.

`knowl init` creates the workspace skeleton, starter policy, root index, and log without replacing existing files. `knowl validate` verifies required workspace paths; full migration, projection, and recovery checks happen during `start` or Host construction. SQL and search state are rebuildable projections and never become canonical content or deletion authority.

The starter schema describes taxonomy, metadata/provenance, links, organization, and contradiction/supersession handling. Knowl reads its first `schema_version:` line and defaults older files without one to version `1`; the version is descriptive, and the remaining prose is not parsed as machine-enforced rules. Code enforces OKF structure, paths, provenance, links, bounds, and protected controls. The exact schema digest must match the provider plan and is rechecked before staging and commit, so operator changes invalidate work planned against older bytes.

## OKF Page Contract and Provenance

- **Identity and metadata**: A safe bundle-relative path without `.md` defines `knowl.id`. `type` is required; `title`, `description`, `resource`, `tags`, `sources`, lifecycle, generation, and verification fields follow OKF v0.2. Unknown producer fields round-trip as extensions. The starter taxonomy is entities/concepts/syntheses, while the application accepts safe semantic Markdown paths under `wiki/` and the operator schema defines intended page types.
- **Citations**: Every maintained factual page cites accepted raw evidence through `knowl.source_refs`, using `adapter:source-id@version`. Raw manifests retain structured `source_document` metadata: `source_id`, `document_id`, `revision`, and `uri`.
- **Shared evidence**: A page can combine multiple configured sources. Projection resolves citations into a sorted `source_documents` collection without copying source bodies into the page; source filters match any contributing source.
- **Links**: Curated pages use standard relative Markdown links with explicit `.md` destinations, such as `[Architecture](architecture.md)`. Generated links identify confirmed pages. Generated navigational double-bracket links, bundle-root destinations, and escaping paths are rejected; literal code examples retain their syntax. Missing article targets produce navigation warnings rather than OKF structural failures and create no graph edge. Catalog targets must exist. Imported OKF may retain bundle-relative links; legacy wikilinks remain readable. External URLs and assets are excluded from the concept graph.
- **Edit authority**: Maintainer factual edits target unique safe semantic Markdown paths under `wiki/`, cite accepted evidence, carry the current schema digest, and obey configured file/count limits. They cannot modify `schema.md`, `wiki/log.md`, or the reserved legacy `wiki/sources/**` boundary. Source-maintenance navigation uses additive catalog memberships rather than arbitrary `index.md` edits.

A successful complete source scan can tombstone a missing document while retaining its raw revisions and curated semantic pages. An interrupted scan never authorizes deletion.

## Browser Inspection and Provenance Safety

The optional web UI reads current canonical pages and immutable accepted raw revisions without fetching upstream content. Missing, oversized, or non-text saved revisions fail explicitly rather than being replaced by newer upstream documents. Citations support the page as a whole, without claim-level attribution; current pages and historical raw revisions retain independent identities.

Search cards preserve retrieved snippets and source refs. Opening a current page reads its content at that moment, which may differ from the search snapshot. Page digests and snapshot versions identify current reads; the browser does not provide historical wiki-page storage. Accepted raw revisions retain their own immutable identity.

"Open original" links require safe HTTP/HTTPS provenance URIs, with credentials, query strings, and fragments removed. Filesystem paths and unsafe schemes are not clickable originals, and deliberate navigation does not forward the operator bearer token. Saved text can remain readable when no original URL is available.

## Export and Canonical Migration

`knowl export okf --output ./public` exports the canonical wiki as a standalone OKF v0.2 directory. The destination must not already exist. Its root contains `index.md`, `log.md`, catalogs, pages, and bundle assets, excluding `schema.md`, `raw/`, `.knowl/`, configuration, and database files. It creates no archive and has no separate `export wiki` alias because `wiki/` already is the bundle.

`knowl export llms-txt --output ./public/llms.txt` produces bundle-relative navigation following the evolving llms.txt v2 open proposal rather than a formal standard. Without `--output`, it writes only the Markdown document to stdout. `--title` overrides the root title; `--summary` adds a blockquote summary. A known publication location can be supplied explicitly:

```bash
knowl export llms-txt \
  --base-url https://docs.example.com/knowledge/ \
  --output ./llms.txt
```

The bundle and bundle-relative navigation can be published together. Knowl creates export files but does not upload, serve, compress, or otherwise publish them, and does not commit or push generated workspaces to Git. Operators own version control and publication.

`knowl migrate okf-v0.2` explicitly preflights and journals legacy conversion, preserves the exact legacy log in an audit archive, commits its marker last, and rebuilds projections. Stop writers and back up first, then run:

```bash
knowl migrate okf-v0.2
knowl validate
```

Re-running migration is a no-op; interruption is resolved deterministically by rollback or completion from the journal on the next invocation. Host startup and read-only commands reject legacy canonical state rather than migrating it implicitly.

## Control Pages

The root `wiki/index.md` declares OKF version `0.2`. Nested `index.md` catalogs cannot redeclare the bundle version. Catalog controls are validated and participate in canonical digests but never become retrieval evidence.

Every `log.md` is a reserved newest-first, ISO-date-grouped OKF control. Each committed operation adds one structured JSON metadata line to the application-owned `wiki/log.md`, recording operation ID, generation, schema digest, source refs, and committed paths. The log's prior digest is a staging/commit precondition; maintainers cannot replace it through arbitrary edits. Source OKF logs remain immutable raw evidence and are not imported into the semantic wiki. Attested Computation declarations remain inert metadata.

## Explicit Hierarchy Reconciliation

`knowl hierarchy reconcile` owns the root catalog and generated `wiki/catalogs/**/index.md` navigation under planner identity `hierarchy-v3`. A valid flat root remains compatible until explicit reconciliation. The planner organizes by subject, gives ordinary pages primary placements, and adds secondary memberships sparingly. Broad heterogeneous domains are recursively decomposed; type, document kind, and technology provide supporting signals. Suitable semantic paths and unrelated memberships are preserved where possible. Semantic quality remains provider-dependent, without a numeric balance or optimal-taxonomy guarantee.

Taxonomy input contains deterministically ordered page IDs/paths/digests, OKF type/title/description/tags, bounded user-content excerpts, and current memberships. It excludes raw source bodies, source-native paths, frontmatter, and provenance envelopes. The schema digest binds the operation, but schema content does not supply taxonomy policy. The provider returns a complete graph for application validation and rendering; hierarchy does not accept `catalog_additions`.

Reconciliation may create, replace, or remove managed root/generated catalogs. It cannot change factual pages, raw evidence, `wiki/log.md` as a provider edit, `wiki/sources/**`, or operator-created catalogs elsewhere. All ordinary pages and returned catalogs must be root-reachable, internal targets must resolve, and the graph must be acyclic. Generated non-root catalogs require children; an empty root is allowed only for an empty wiki. Cross-catalog membership is possible, but duplicate child edges within a catalog are invalid.

Bounds are 1,024 ordinary pages, 1,024 catalogs, 16,384 edges, depth 16, 4 MiB planner input, 4,096 excerpt characters per page, 1 MiB output, 1,024 managed edits, 256 KiB per generated catalog, and a 1 MiB stage manifest. Paths use the safe edit-path bound. The complete input must fit; oversized input fails before provider invocation rather than classifying a partial wiki.

The command reserves/claims one durable hierarchy operation, stages schema/snapshot/target-digest preconditions, commits through recovery journaling, appends the audit log, updates projection, and validates the result. JSON includes `operation_id`, `status`, and `changed`, plus `generation` and `files` for canonical changes. Converged replay is byte-stable. Invalid/stale graphs, bounds, staging, and projection failures retain specific wrapped causes; interrupted commits replay the durable stage/receipt.

The command starts no HTTP listener, general scheduler, source jobs, or `sync.on_start` work. Startup, bootstrap, validate, retrieve, operation reads, source list, and source status do not invoke hierarchy reconciliation. Earlier planner catalogs remain valid until explicit reconciliation.

## Source Maintenance Contract v6

`MaintenanceInput.contract_version` is `source-maintenance-v6`. Selected ordinary pages contain complete factual `Content` once; the wire encoder omits `Body`, although application snapshots retain both fields. Catalogs contain the complete bounded navigation graph as `HierarchyCatalog` values with canonical `path`, original `digest`, `title`, and sorted `children`. The root comes first; remaining catalogs are path-sorted. Catalog Markdown remains in the application, and catalog bounds are independent of the factual `ReadLimits.Pages` default of 20.

### Additive Navigation and Publication

Source plans submit `ModelEditPlan.CatalogAdditions`, encoded as `catalog_additions`:

- Existing catalogs require their original `expected_digest` and omit `title`.
- New catalogs require an empty digest, a safe nonempty single-line title, and children.
- Children use canonical wiki paths and must already exist or be created by the same plan.
- Duplicate memberships are no-ops; duplicate entries for one catalog are invalid.

The application appends escaped links to authoritative catalog originals, preserving unrelated prose and links. It validates the final graph and combined factual/catalog mutations before staging. New catalogs and edited factual pages must be root-reachable. Cycles, invalid targets, exceeded bounds, and stale digests cannot commit. Arbitrary model `index.md` FileEdits are rejected, including supplied `FilePlan` calls.

### Complete Request Budgets

The default and supported local request ceiling is 4,194,304 bytes. Assembly measures the exact serialized envelope and, for the built-in runtime, the complete current structured prompt including instructions, schemas, and framing. The effective cap is the smaller application/provider declaration, captured with a non-secret format identity before operation reservation.

Complete source text, schema, and the bounded catalog graph are indispensable. If they alone exceed the cap, assembly fails with `input_budget/required_input_limit` before inference or staging while immutable raw evidence remains accepted.

Ordinary candidates are considered in existing priority order under per-page, count, and deadline bounds. Controls and duplicates do not consume the factual allowance. A page that does not fit is omitted; later smaller pages may still fit. Editable pages are never excerpted. Existing factual edits must target an included complete snapshot and copy its exact digest, including supplied `FilePlan` calls. Canonical preconditions protect against later changes.

`MaintenanceRequestSizer` lets embedded maintainers declare pure capacity, format identity, and exact request size. Plain custom maintainers assume the shared JSON envelope; wrappers must implement sizing for their complete requests. The built-in runtime also checks the actual prompt before invoking the inner agent. These limits do not describe token capacity, remote HTTP serialization, session history, or total workspace RAM. `IngestResult.Budget` is transient typed byte/count evidence; operation context reports persist measured fitting facts.

Contract version, schema digest, effective request cap/format identity, read/plan/catalog limits, and output-affecting retrieval policy participate in maintenance-policy generation. Incompatible unplanned work fails before inference with `maintenance_policy_mismatch`. Already validated v1/v2/v3/v4/v5 concrete stages resume under canonical preconditions without embedding or maintainer inference; terminal operations remain replayable.

## Bounded Output Correction

`knowl.output.max_corrections` defaults to one replacement and accepts only zero or one. The built-in runtime shares one loop across structured/schema/branch and full application validation. Optional `ValidatingMaintainer` and `ValidatingHierarchyMaintainer` capabilities receive synchronous validators over captured inputs. The application validates the returned candidate again before staging. Base custom adapters remain single-call; a supplied `FilePlan` invokes no provider.

All turns share a 1 MiB raw collector-text bound and five-minute context, including exclusive-runtime wait and lazy setup. Earlier caller stops take precedence. Wrapper retries remain zero; transport retries belong to the scheduler. Feedback contains one safe typed validation code over unchanged original input, with a 128-byte request reservation; initial usage excludes the reserve. Invalid output, error strings, and rationale are not copied into feedback. Output limits, timeout, transport failure, and canonical conflicts cannot trigger another generation.

One validated, content-free correction report is finalized per work attempt before staging or on planning failure, with a five-second durable write timeout. Failure to save an accepted report prevents staging; failure to save an error report preserves the original classified cause. Reports include known physical turns, corrections, prefix bytes, allowance/limits, outcome, and last safe validation code; unknown measurements are absent. Effective support and limits fingerprint source generation and hierarchy planner identity. Authenticated stages recover without inference before the current planner fence, subject to canonical preconditions.

## Generic Literal Retrieval

One scanner identifies Unicode letter/number words with attached combining marks. Identity uses NFC, `strings.ToLower`, then NFC. There is no language detection, full case folding, accent stripping, stemming, or transliteration. `CAFÉ` matches `Café`, while `cafe` remains distinct. Question words participate like other words. Inflection and paraphrase recall remain lexical limitations.

Normalized words encode as lowercase unpadded base32 of UTF-8 bytes prefixed with `k`. SQLite stores these tokens in existing FTS semantic columns; PostgreSQL builds its existing A/B/C/D-weighted `search_vector` from transient encoded fields in the same transaction as original values. No extra persistent text fields or hashes are required. Native AND retrieval precedes OR retrieval, fillers are deduplicated, and path ties are deterministic. Native scores and PostgreSQL position/ranking limits still apply.

Both stores prioritize title, normalized OKF tags, OKF description, then user-authored body. Paths, filenames, extensions, provenance, and transport metadata do not enter ranking or snippets. The stored OKF object remains intact. Matching, field priority, filters, strict-then-relaxed behavior, and path ties are shared; other result ordering may differ because native scores differ.

Evidence uses original semantic fields and rune spans, preserving spelling, case, and combining marks. Native snippet/headline generation is excluded; tag-only evidence retains `tag:`. Encoder and query guards are finite. Failed projection rolls back without partial readiness or truncating supported indexed words.

Migration 15 invalidates readiness and clears incompatible derived tokens while preserving original page values and durable history. A complete rebuild restores scoped readiness transactionally. Down restores the earlier projection layout and invalidates readiness; rollback requires a compatible binary, migration direction, and fresh rebuild.

## Source Signals and Context Selection

Ingest extracts detached `SourceSummary` title, tags, headings, and body from accepted UTF-8 text before selecting context. Valid leading YAML `title` strings and `tags` sequences take precedence. Otherwise the first eligible ATX/Setext heading supplies the title, followed by the first eligible prose line. Fenced and indented code are excluded. This is a documented Markdown subset rather than full CommonMark. Extraction neither rewrites raw bytes nor summarizes with a model; provider `SourceText` stays complete under read limits.

Title, tags, headings, then body share 4,096 runes / 16,384 UTF-8 bytes. Titles and individual list entries are capped at 256 runes; each list examines at most 32 entries. Body uses the beginning of eligible prose within the remaining budget and may omit useful later terms. Metadata parsing is separately bounded.

SQLite and PostgreSQL construct the same lexical query in field priority order, capped at 32 terms and 256 normalized term runes. Direct index callers require valid UTF-8 and at most 64 KiB combined examined raw signals before clipping; only the first 32 tags/headings are examined. Source ID and adapter are bounded fallback inputs only when semantic fields provide no usable terms. No hits do not trigger identity fallback. Neighbor/root/recent merging, scope isolation, and page limits remain active. Enabled embeddings independently use bounded original semantic fields, with identity fallback only when semantic text is empty.

## Optional Hybrid Retrieval

`knowl.embeddings` independently selects an OpenAI-compatible embedding API or the inert default. SQLite and PostgreSQL share semantic preparation and bounded reciprocal rank fusion (RRF). Exhaustive page maxima use native database true cosine through bundled CGO-free sqlite-vec or optional operator-owned pgvector. There is no language-specific route, in-process model, or ANN index. PostgreSQL lexical retrieval requires no pgvector extension; Knowl never installs it or falls back to the old Go full-vector ranking path. Embeddings match related wording but neither generate answers nor maintain pages. Project-local CLI, MCP/HTTP service, and embedded Go applications share retrieval behavior; the maintainer remains separately configured.

### Embedding API Identity

The operator must keep the declared immutable model revision aligned with the served weights. Responses are checked for model name and dimensions, but the generic API cannot attest weight identity. Changing served weights requires changing the declared revision and rebuilding. Disabled embeddings create no client, read no embedding credentials, and make no model/readiness requests.

The pinned E5-base reference uses mean float32 output, required `query: ` and `passage: ` prefixes, and disabled server truncation. Rune limits are not tokenizer limits: an embedding service must reject token overflow instead of silently truncating; the pinned TEI reports `input_limit`.

### Semantic Preparation

Inputs preserve original case, use NFC, and normalize line endings. Page chunks follow heading sections and block boundaries, with at most 384 runes including semantic context/prefix, continuing to the end of each eligible page. Long sections split at block boundaries; oversized blocks split into bounded pieces. Heading ancestry, title, and semantic metadata accompany page inputs, while dense citations retain original document text. API calls contain at most 16 inputs. Queries/source signals use at most four 384-rune windows with 64-rune overlap and report omitted coverage. Prefixes apply once. Private lexical tokens, paths, and provenance do not enter the model. Chunks remain retrieval projections and do not replace complete source or factual-page authority in maintenance.

This section-aware page preparation supersedes the earlier fixed-overlap description for pages; the 64-rune overlap applies to query/source-signal windows. Ready page projections have zero coverage omissions; bounded queries and source signals may report omissions. Lexical full-field indexing and complete-page maintenance limits remain independent.

### Native Storage, Publication, and Reads

Network inference runs outside SQL locks and transactions. Rebuild commits lexical state first, invalidates dense readiness, and publishes a complete bounded vector projection only if the canonical snapshot still matches. SQLite stores portable little-endian float32 BLOBs. PostgreSQL's optional SQL stream uses the extension-owned unbounded vector type and retains the scoped advisory lock for lexical replacement/publication.

Base SQL migration 26 invalidates legacy derived dense state and adds native format. Optional PostgreSQL SQL migration 1 follows extension-free base Up under a separate standard Goose history; optional Down precedes base Down. New schema transitions use SQL. Normal Goose-on-Open and historical Go migration 19 remain supported. Existing empty-history corruption is never automatically bootstrapped or repaired.

Knowl owns supported SQL writes. Publication, reuse, and recovery fully validate coverage, vectors, model space, dimensions, counts, and budgets before atomic ready publication. Lexical page changes invalidate incompatible readiness. Search, context selection, startup, and lint admit existing scalar readiness, snapshot, and active model state without repeating full coverage or vector audits. Search keeps lexical candidates, filters, and consumed original evidence in one consistent read transaction, validating current page digest, winning ordinal, and content hash where evidence is consumed. Ordinary reads do not transfer stored vectors into Go. Per-read detection of arbitrary out-of-band corruption in nonselected rows lies outside this trust boundary; startup and lint provide admission/repair checks rather than offline integrity audits.

This trust refinement reuses existing validated native-format readiness and requires no new schema migration, cache, setting, or rebuild solely for the refinement.

### PostgreSQL Configuration and Migration Authority

PostgreSQL configuration uses `knowl.storage.type: postgres` and `knowl.storage.postgres.dsn`. The checked-in type has `dsn`, not `dsn_env`. Replace the SQLite subsection while preserving maintainer, sources, and authentication. A placeholder configuration is:

```yaml
knowl:
  storage:
    type: postgres
    postgres:
      dsn: 'postgres://KNOWL_RUNTIME@DB_HOST/KNOWL_DB?sslmode=verify-full'
```

Use deployment credential facilities and keep real credentials and `.config/knowl/*.local.yaml` out of source control. Embedding `api_key_env` supplies the separate embedding credential, not SQL authentication.

Opening PostgreSQL runs two standard embedded Goose streams:

| Stream | Migration sources | History | Native transition |
| --- | --- | --- | --- |
| Base, always considered on Open | `pkg/knowl/store/postgres/migrations` plus historical Go migration 19 | `goose_db_version` | SQL 26 adds `native_format` and invalidates derived dense state without requiring an extension |
| Optional, enabled dense with verified pgvector | `pkg/knowl/store/postgres/pgvector_migrations` | `goose_pgvector_db_version` | SQL 1 replaces derived BYTEA vectors with extension-owned unbounded `vector` and a dimension constraint |

Base Up precedes optional Up. An unavailable extension leaves optional SQL pending; lexical-only operation remains usable without creating optional history. Provisioning followed by reopen with embeddings enabled retries pending SQL; rebuild the derived projection afterward. Queries/rebuilds neither install the extension nor repair histories, and repeated queries do not clear cached unavailable admission.

Dense admission resolves extension-owned types, functions, and operators through PostgreSQL catalogs, including trusted nondefault extension schemas; same-named search-path objects are not trusted. Local testing used PostgreSQL 16.14 with pgvector 0.8.2. No ANN index or extra dimensions/capacity setting is required.

### PostgreSQL Provisioning and Verification

Stop all hosts/writers, including old binaries, and back up canonical/raw data plus the database and both migration histories. Use a compatible Knowl binary with migration authority to initialize or upgrade the base through 26; ordinary Open registers historical Go migration 19. A standalone SQL-directory Goose invocation does not replace fresh base initialization or that Go migration. Do not run concurrent migrators.

An appropriately authorized operator provisions pgvector through the deployment's supported package/service procedure. For a default trusted schema, provisioning SQL is:

```sql
CREATE EXTENSION vector;
```

This is external operator provisioning, not Knowl startup/base-migration SQL. Verify already-provisioned deployments rather than reinstalling.

After base initialization, the optional SQL-only stream supports standard Goose CLI commands compatible with checked-in library v3.27.3. Supply `GOOSE_DBSTRING` securely for the intended database/migration role, disable shell tracing, and treat migration logs as sensitive. `-env none` avoids loading an unintended `.env`. These commands apply only to genuinely absent fresh history or recognized valid history, never existing empty/corrupt history:

```sh
GOOSE_DRIVER=postgres goose -env none \
  -dir pkg/knowl/store/postgres/pgvector_migrations \
  -table goose_pgvector_db_version status
GOOSE_DRIVER=postgres goose -env none \
  -dir pkg/knowl/store/postgres/pgvector_migrations \
  -table goose_pgvector_db_version up
```

Migration authority needs history creation/use, owned embedding-table/constraint alteration, and extension schema/type/function access. A runtime role can instead use fully pre-applied recognized current histories with ordinary table/sequence access, schema usage/native function execution, and SELECT on both Goose histories. Runtime need not own tables or install extensions. Grant optional-history SELECT after pre-application; do not create an empty history as a permission workaround. Denied pending DDL remains typed `unavailable` or configured lexical degradation until privileged pre-application and reopen.

Reopen with the matching binary, explicitly rebuild or complete startup rebuild, and verify actual semantic retrieval reports/evidence before resuming writers. `/readyz` alone does not establish dense readiness. Do not run old/new writers concurrently across the format transition.

### PostgreSQL Failure and Rollback

Transactional optional SQL failures roll back schema, data, and history. With valid standard version 0 history, fixing the external cause allows ordinary Up on later reopen. An existing but empty `goose_pgvector_db_version` is corrupt rather than fresh: admission fails closed with redacted typed `unavailable` and leaves it unchanged. Unknown, duplicate, inconsistent histories and unrecognized schemas also cannot become ready native state.

Keep hosts stopped and recover externally using trusted deployment evidence or a matching known-good database/history restore. Do not fabricate version 0, drop/recreate history, use `-no-versioning`, or introduce an alternate bootstrap. Supported recovery returns to ordinary Goose Up and read-only history/schema validation; reopen/rebuild afterward. Preserve canonical pages, raw evidence, source state, and operations.

Rollback stops writers and backs up first. Optional Down must precede base SQL 26 Down. For optional current version 1:

```sh
GOOSE_DRIVER=postgres goose -env none \
  -dir pkg/knowl/store/postgres/pgvector_migrations \
  -table goose_pgvector_db_version down
```

Optional Down invalidates dense state and returns vectors to BYTEA. Base 26 Down refuses the wrong order while native type remains. Use a matching Knowl migration provider/binary for base directions, especially across Go migration 19. Neither step removes the operator's extension or canonical/raw/source/operation data. Pair rolled-back schema with a compatible binary and rebuild; schema rollback does not imply vector reuse. Do not reopen a new dense-enabled binary midway, since ordinary optional Up can reapply the transition.

### Ranking and Evidence

SQL computes each eligible page's maximum native true cosine across every query window and chunk before limiting page candidates. Equal native scores select the lowest winning chunk ordinal, then bytewise page identity. Close unequal scores retain native order without a production epsilon. Older Go near-unit dot/backends may produce different ordering or evidence. Lexical ordering and bounded fusion remain unchanged.

Each channel retains `min(100, max(20, 4*k))` candidates. Equal-weight RRF uses ranks starting at one and constant 60, deduplicates pages per channel, and breaks fused ties by page ID. Fused relevance seeds enter the existing neighbor/root/recent context policy. Lexical-only mode preserves native lexical ordering and snippets. A result found by both channels retains its lexical snippet; dense-only evidence comes from the winning original chunk, verified against indexed page digest and chunk hash. Semantic similarity does not establish factual agreement.

Zero stored-vector transfer is not a latency guarantee. Documented comparisons report mixed current latency, including PostgreSQL source-filter regressions, alongside original v1.3 material regressions and reduced Go allocation volume. Separately labeled isolated 100000-chunk kernels omit production integrity checks and caps on both sides and do not establish supported capacity or faster complete queries.

### Capacity, Failure, and Recovery

Per-scope limits remain 8,192 chunks, 1 MiB coverage metadata, and 64 MiB total projection data. The 64 MiB bound accounts for logical float32 vector payload and bounded metadata, excluding additional PostgreSQL native-storage headers. Dimensions 1–4,096 support exact scalar retrieval without an ANN index, chunk shortlist, or increased capacity. Exhaustive scoring remains database work proportional to the eligible population and query windows. `ScannedChunks` reports the admitted published full-scope chunk count before source filtering; it is neither a fresh integrity audit nor the SQL candidate count. Larger corpora require a separately evaluated capacity design.

Ready state represents a fully validated complete projection. Publication rejects incompatible, incomplete, or over-capacity data; reads reject incompatible readiness/snapshot/model state and invalid consumed evidence.

`failure_policy: lexical` produces explicitly degraded lexical results for classified embedding failures; `strict` returns typed failures. A classified rebuild failure saves degraded state even under strict policy. Invalid input and caller cancellation never become successful fallback. Queries do not rebuild projections or download models. Startup retries a current degraded projection once; after repair, operators can restart or embedded applications can invoke `knowl.RebuildProjection(ctx, config, snapshot)`.

A model-space fingerprint binds model, immutable revision, dimensions, prefixes, preprocessing, and normalization. Source-maintenance-v6 also binds fusion, candidate/capacity, and failure policy. Addresses, credentials, and CPU settings do not identify output policy. Space changes require a complete rebuild. The structured page input version also changes model-space identity and causes existing dense state to rebuild once on startup before later reuse. Historical staged recovery publishes lexical state without new inference, with dense repair performed separately. Old unplanned generations reject before embedding or maintainer calls.

Projection refresh updates lexical search, links, and provenance. Prior vectors are reused only when model space, complete prior state, and prepared input match. Unchanged snapshots or source-only revisions need no new page embeddings; changed sections require them. An explicit rebuild computes every page vector again in batches of at most 16 without SQL locks during inference. Snapshot conflicts discard stale builds. A strict projection error can occur after canonical commit; repair the derived projection while preserving committed facts and raw evidence. Disabling embeddings ignores derived vectors and retains default lexical behavior.

Go Query/Ingest results expose per-call `RetrievalReport`. Maintenance persists a bounded report before inference, including failed selection, with its producing operation attempt. `QueryResult.Retrieval`, `IngestResult.Retrieval`, and durable `Operation.Retrieval` include requested mode, model-space prefix, candidate/scanned-chunk counts, and omitted coverage. `Operation.RetrievalAttempt` identifies the originating attempt; terminal or legacy replay does not invent or replace historical reports. Reports omit text, endpoints, credentials, and upstream error bodies. HTTP/MCP expose optional safe `retrieval.effective` and `retrieval.reason`; operation `details` also expose the bounded stored report and attempt identity.

### Local TEI Reference Deployment

The base service profile is lexical-only. From the current checkout, the opt-in command is:

```bash
docker compose -f deploy/sidecar/compose.yaml \
  -f deploy/sidecar/embeddings.compose.yaml up --build -d
```

The overlay mounts `deploy/sidecar/embeddings.yaml` as a complete replacement configuration, preserving the OpenAI maintainer, engineering source, storage, and operator token. Customized base settings must be carried into that complete configuration; individual keys are not merged. The historical v0.5.0 quickstart is a separate recipe.

The private `tei` service uses pinned Linux `amd64` CPU TEI 1.9.0 and E5-base revision `d128750597153bb5987e10b1c3493a34e5a4502a`, returning 768 normalized dimensions. The overlay records the exact image digest. TEI publishes no host port, uses no GPU, and shares Knowl's private Compose network. Mean float32 output, required prefixes, no second server prompt, and disabled automatic truncation match the client contract. Other profiles need their own runtime and quality validation.

First startup downloads the model into persistent `tei-model-cache`; retaining it permits offline restarts. The reference allocates two CPU cores, 4 GiB memory, and a ten-minute cold-start health allowance. Recorded observations were 194.5 seconds cold readiness, 6.6 seconds cached restart, 1,075 MiB model cache, and approximately 1.91 GiB idle memory. These measurements are neither hardware minima nor latency guarantees.

Inspect TEI with:

```bash
docker compose -f deploy/sidecar/compose.yaml \
  -f deploy/sidecar/embeddings.compose.yaml ps tei
```

Explicit lexical fallback allows Knowl to start during cold loading. For strict policy, wait for TEI health and model loading before starting Knowl. After degraded startup, restart Knowl once TEI is healthy to rebuild dense state; queries do not repair it. Compose `down` preserves named volumes.

For project-local Knowl, run TEI separately, publish its port only on host loopback, and use a host endpoint ending in `/v1/embeddings`. The Compose `tei` hostname does not resolve from a host process. Client and server must agree on model, revision, dimensions, and prefixes. See [Service Operations](service-operations.md#optional-embeddings-and-hybrid-retrieval) for embedding API configuration and operational limits.

## Operational Reports and Staging Recovery

Canonical commits write staging manifests and preimage journals before atomic replacement. Startup handles recovery before readiness: `prepared` journals restore preimages and record rolled-back operations; `committed` journals are cleaned after canonical files are complete; incomplete staging is discarded as uncommitted work. Shutdown preserves interrupted operations for recovery and redacted inspection. Staging/recovery files must not be hand-edited while a Host runs.

Source synchronization shares the staged canonical writer and journal. Filesystem recovery precedes resumable source runs and projection preparation. One source failure preserves other sources' raw evidence and the prior successful semantic snapshot. Sync success means raw acceptance and durable maintenance reservation; required maintainer execution is asynchronous, with bounded outcomes separately reported in source status.

For backups, stop the Host and copy the complete workspace; its Markdown snapshot can rebuild the SQL projection. Operators commonly version `schema.md`, `raw/`, and `wiki/`, treating `.knowl/` as local operational state. Deliberately versioned operational state requires credential exclusion and review of database/staging contents. Absence from search is not an audited delete or implicit forgetting operation.

### Attempt-Bound Context Evidence

Source maintenance finalizes one immutable context snapshot per work attempt after assembly, before inference, or before returning a measured selection/catalog/assembly failure. It records eligible unique ordinary IDs, their first actual selection phase, fitting dispositions, observed catalog count, serialized request budget, and same-pass projection checks. A fully assembled request remains `assembled` if later cancellation or inference fails. Failures before selection and crashes before finalization may leave no report; reads never reconstruct missing facts.

Optional `DiagnosticContextIndex` provides same-pass metadata, while `OperationContextReportStore` persists attempt-bound snapshots. Custom indexes lacking metadata use `unknown` reasons. Custom stores without the writer retain only transient `IngestResult.Context`. Built-in stores prevent new inference if report persistence fails. If assembly and persistence both fail, the original classified assembly cause remains authoritative.

Migration 17 adds nullable `context_report` and `plan_file_count` to both stores. Scope/current-attempt/terminal guards protect writes; identical repeats are idempotent. Historical attempts remain explicit. Legacy null file counts stay unknown; opaque legacy digests remain readable without being presented as SHA-256 plan summaries.

HTTP/MCP share an allowlist excluding source/query/prompt text, edits, rationale, credentials, endpoints, and upstream messages. Context evidence is bounded to 32 KiB and 100 entries, with identifiers limited to 2,048 UTF-8 bytes. Together with bounded retrieval/warnings, serialized details stay below 96 KiB. Reporting limits do not change model pages or measured counts. Downgrade discards only new context/file-count evidence.

Migration 18 adds nullable `correction_report` without backfill. Strict codecs and bounded SQL reads reject corrupt or future-attempt evidence before claim counters can legitimize it. Identical writes are idempotent, with scope/current-attempt/terminal guards. The allowlist exposes at most 1 KiB correction evidence within the combined 96 KiB ceiling. Downgrade drops only this observational column, preserving raw data, canonical content, and stages.

## Gradual Configured-Source Retirement

Removing a configured source differs from tombstoning a deleted upstream document. An exact `adapter` plus `source_id` owner transitions through durable `active`, `retiring`, and `deleted` states. Replacing filesystem with Git creates an independent owner: equal IDs, paths, revisions, contents, or page IDs do not establish aliases, lineage inheritance, or provenance rewrites.

Additions first observed with removals must complete normal accepted synchronization and the maintenance-readiness checkpoint before retirement page work becomes eligible. Pure removal has no replacement gate.

Retirement uses bounded page operations discovered through stable canonical-citation windows. An unchanged pre-transition page can still expose its retiring ref, but every new publication is checked against current lifecycle authority and cannot carry that ref forward. Old-only pages are deleted with the minimum required catalog closure. Mixed pages are recomputed from all and only active accepted evidence, potentially retaining their catalog membership. Exact page/catalog digests are checked again at commit. Failed pages remain unchanged with structured path/target diagnostics, including reasons behind `plan.empty_safe_subset`, and can be retried while independent pages commit. Readers may see reconciled and unreconciled pages together until a fresh authoritative bounded zero-reference round and completion of page/projection work mark the owner `deleted`.

Immutable raw revisions remain historical evidence but cannot authorize new publications from a retired owner. Consistency covers one page plus required catalog mutations under the existing journal, preimages, projection replay, and in-process publication coordinator. There is no full affected-page manifest, global wiki cutover, global rollback, or filesystem/search joint transaction. The supported writer boundary is one Host/coordinator per workspace writer set; distributed filesystem writers need external exclusion. Restoring the exact configured source cancels future claims by lifecycle generation. Already reconciled pages remain valid and are rebuilt only through normal synchronization/maintenance. Preserve the operational database and prefer rolling forward; binary downgrade requires stopped writers, and lifecycle rows/canonical files are not manually rewritten for recovery.

Document tombstoning alone does not implicitly erase accumulated knowledge or retire a still-configured owner. Successful synchronization also removes only the matching source's legacy derived `wiki/sources/<source_id>/**` subtree through staged recovery while preserving raw revisions and curated pages.

## External and Runtime Authority

- **Host**: Owns session/user context, final answers, tool orchestration, durable-event decisions, immutable revision assignment, and trusted-scope mapping. Public callers cannot override scope. Source-system ownership and workflows remain outside Knowl.
- **Connectors and adapters**: Connectors translate inputs into public ingest requests. Filesystem/Git adapters list and fetch configured documents but cannot directly write workspace/SQL, choose arbitrary trusted scopes, or submit ready-made canonical changesets. Git resolves one immutable commit per scan, consumes regular blobs only, never executes repository content, and never modifies the remote. Interrupted scans do not authorize deletions.
- **Maintainer**: Selected by `knowl.provider` from `runtime.providers`, or explicitly injected for embedded tests. Host construction fails before readiness when neither exists. Providers receive bounded untrusted context and structured-output constraints, with no unrestricted filesystem authority; proposals are validated before canonical mutation.
- **Attested Computation**: OKF runtime, parameters, computation, executor, and attester declarations are parsed, preserved, projected, and returned as inert data. Knowl does not load their resources or execute declared programs.

Supported Go imports are `pkg/knowl` for plain-Go composition, `pkg/knowlfx` for Fx lifecycle, `pkg/knowl/mcp` for MCP, and `pkg/knowl/types` for transport-neutral domain types. Other packages implement those surfaces. App policy owns consuming ports; adapters depend inward on it. `pkg/knowl` alone composes multiple adapters; Fx and CLI do not become separate composition roots.

Bootstrap is an optional CLI preflight that checks freshness and path separation, initializes local workspace/configuration, constructs a deterministic filesystem source, and calls `Host.SyncSource` once. Ordinary sync has no freshness requirement: it accepts immutable raw evidence and reserves idempotent semantic maintenance work rather than copying sources into the wiki.

## Maintenance Execution Ownership

`knowl.workers` selects one or two fixed owners. Each has an independent RuntimeMaintainer, agent, session service, and source/hierarchy services, while sharing policy identity and canonical storage. A finite dispatcher runs bounded worker loops. Admission precedes durable claims; each active claim renews its lease. Background workers, joined Drain calls, direct hierarchy, and optional model-lint share this pool.

Inference and correction hold only their execution owner. One Host publication gate serializes canonical commits, snapshot projections, and outcomes, including source-sync saga recovery and hierarchy no-op finalization. Filesystem locks, expected digests, provenance validation, and recovery journals remain active. Stale log/catalog/page preimages fail with a permanent typed conflict without automatic replanning. Explicit rebuilds remain preflight or stopped-writer operations.

Shutdown closes admission before joining users. When the active shutdown caller's bound expires, execution lifetimes are canceled and live resources are retained for a later Stop. Concurrent Stop callers wait cancelably without interrupting that caller's graceful drain. Cleanup retains failed components and does not repeat successful cleanup. Failed setup cannot allocate another runtime while prior cleanup remains unresolved.

See [Architecture](architecture.md) for overall design, [Public Contract](public-contract.md) for service APIs, and [Service Operations](service-operations.md) for configuration and operational commands.
