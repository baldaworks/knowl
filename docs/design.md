# Product design

**Durable project knowledge for agents.**

Knowl is a self-hosted LLM wiki for agentic applications. It turns durable
sources into an inspectable Markdown wiki with OKF-compatible storage and
returns bounded, provenance-backed evidence.

The host decides which events are durable, assigns their immutable source
revisions, orchestrates tools, and generates the final user answer. Knowl owns
durable acceptance, canonical raw and Markdown artifacts, operation recovery,
and bounded evidence with source references. The configured maintainer
provider proposes validated Markdown updates inside Knowl; it is neither a
connector nor another public interface.

The default deployment is a sidecar service with SQLite. Agents use MCP; HTTP
is the deterministic control transport. Go applications may use Fx to run the
same host runtime in-process.

## Product boundary

Knowl supports these baseline use cases:

1. Bootstrap an existing Markdown wiki, Obsidian vault, or OKF v0.2 bundle into
   a Knowl-owned workspace as the first production filesystem-source sync.
2. Ingest supplied text or a URI reference through one canonical pipeline.
3. Retrieve bounded evidence and provenance for a host query.
4. Read durable ingest-operation status.

Knowl is not session, user-fact, or temporal memory; agent orchestration; the
primary final-answer generator; a generic memory platform; or a multi-tenant
control plane.

### Public business contract

The contract has exactly three business operations:

- retrieve bounded evidence;
- ingest one source;
- read one durable operation status.

MCP is the primary agent-facing interface:

- `knowl_retrieve`
- `knowl_ingest`
- `knowl_operation`

HTTP/OpenAPI provides the same deterministic contract:

- `GET /v1/retrieve`
- `POST /v1/ingest`
- `GET /v1/operations/{operation_id}`

`GET /healthz` and `GET /readyz` are operational endpoints, not additional
business operations. The authoritative HTTP schema is
[api/openapi/knowl.yaml](../api/openapi/knowl.yaml).

Neither transport exposes direct page CRUD, raw workspace writes, search
sub-steps, or public review/apply choreography. The operator CLI is a local
convenience wrapper over the same operations, not a primary agent interface.

## Content and trust boundaries

The filesystem workspace is the canonical content owner:

- `raw/` stores immutable accepted source versions;
- `wiki/` stores only the human-readable, directly portable semantic OKF v0.2
  bundle maintained by Knowl;
- `schema.md` is operator-owned Markdown policy supplied to the maintainer as
  untrusted guidance, not an executable validation schema;
- `wiki/index.md` declares the bundle version and, with every reserved
  `index.md`/`log.md`, is control content rather than retrieval evidence.

SQL and search state are operational and rebuildable, never canonical content.
The lexical projection indexes four semantic fields with descending priority:
title, normalized OKF tags, OKF description, and user-authored body. It excludes
paths and filenames, OKF extensions, source references and documents, and other
provenance or transport metadata from ranking and evidence snippets. SQLite FTS
and PostgreSQL text search share literal matching, field priority, filters,
strict-then-relaxed retrieval and path ties. Their native scores can order other
results differently. The stored OKF object remains intact. Public results optionally expose a safe
retrieval mode and reason.
The detailed filesystem contract is in [workspace.md](workspace.md).

An ingest-side connector may translate text, a URI, origin, and idempotency
hints into one public ingest request. Public ingest accepts exactly one nonempty
`content` or `uri`: the latter stores the URI string as a reference with default
media type `text/uri-list`, without downloading the document. To ingest document
contents, the caller supplies the obtained text in `content`, optionally with
`origin` as a source identity hint. Origin does not trigger a fetch or populate
structured source-document URI metadata automatically. Both transports trim
surrounding whitespace before normalization.

Separately, the built-in read-only
filesystem and remote Git source adapters list and fetch configured documents
for the source reconciler. Git scans resolve a branch or tag once, consume only
regular blobs from that immutable commit, and never execute repository content
or modify the remote. Neither adapter may write the workspace or SQL directly,
select an arbitrary trusted scope, or submit ready-made canonical changesets.

The host owns session and user context, final-answer generation, tool
orchestration, and the mapping to the trusted Knowl scope. Public callers
cannot override that scope. Knowl returns bounded evidence and operation status
only.

Source-system ownership stays outside Knowl. A Balda, Norma, or equivalent host
decides when an ADR, completed story, investigation, issue, pull request, or
runbook has become durable and submits that immutable revision. Knowl does not
own Slack, Telegram, Jira, GitHub, or their workflows, and it does not answer
the user itself.

A configured provider is a required runtime implementation detail. Knowl
resolves `knowl.provider` through the shared `runtime.providers` configuration
and validates returned plans before canonical mutation; embedded tests may
inject an explicit maintainer instead. Host construction fails before readiness
when neither is present. Provider code receives bounded untrusted context and
structured-output constraints, never unrestricted filesystem authority.

### Generic literal retrieval

One scanner finds Unicode letter/number words with attached combining marks.
Each complete word uses NFC, `strings.ToLower`, then NFC for identity; there is
no language detection, full case folding, accent stripping, stemming or
transliteration. Ordinary English/Russian casing and composed/decomposed accents
match: `CAFÉ` retrieves `Café`, while `cafe` remains distinct. Question words
such as `what` and `why` participate like other words. Word forms and paraphrases
remain limitations of the default lexical mode. Optional embeddings provide
semantic candidates through the same generic retrieval path.

The shared encoder represents supported normalized words as lowercase unpadded
base32 of their UTF-8 bytes, prefixed with `k`. This private representation avoids
native tokenizer differences without hashes or extra persistent text fields.
SQLite writes it only into the existing FTS semantic columns. PostgreSQL writes
its existing A/B/C/D-weighted `search_vector` from transient encoded fields in
the same transaction as original page values. Queries use these tokens for
native AND then OR retrieval, deduplicate fillers and retain deterministic path
ties. Native scores and PostgreSQL position/ranking limits still apply.

Evidence comes from original semantic fields and original rune spans; snippets
preserve spelling, case and combining marks. Native snippet/headline generation
is excluded from the retrieval path. Tag-only evidence retains its `tag:` label.
The encoder and query guards have fixed finite bounds, and a failed projection
rolls back without partial readiness or truncating supported indexed words;
see [literal retrieval bounds](operations.md#generic-literal-retrieval).

Both migration 15 upgrades invalidate readiness and clear incompatible derived
search tokens while preserving original page values and durable history. A
complete current rebuild makes the scoped projection ready in its transaction.
Down restores the earlier projection layout and invalidates readiness again;
rollback requires a compatible binary, migration direction and fresh rebuild.

### Source signals and context selection

Ingest extracts detached `SourceSummary` title, tags, headings and body from the
accepted UTF-8 source before selecting context. Valid leading YAML `title`
(string) and `tags` (sequence of strings) take precedence. Without a usable
metadata title, the first eligible ATX/Setext heading supplies the title,
then the first eligible prose line. Fenced and indented code are excluded.
The scanner supports this documented Markdown subset, not full CommonMark.
Parsing neither rewrites raw bytes nor summarizes the source with a model;
provider `SourceText` remains complete under the existing read limits.

The shared wiki clipping policy gives title, tags, headings, then body a total
budget of 4,096 runes / 16,384 UTF-8 bytes. Title and each list entry are capped
at 256 runes; each list examines at most 32 entries. Body is the beginning of
eligible prose within the remaining budget and can omit useful later terms.
Metadata parsing is separately bounded; see [syntax and limits](operations.md#source-signals).

Both SQLite and PostgreSQL build the same lexical query from those fields in
priority order, capped at 32 terms and 256 total normalized term runes.
Direct index callers are checked for valid UTF-8 and a combined 64 KiB of
examined raw signals before clipping; only the first 32 tags/headings are examined. Source ID and
adapter are bounded fallback inputs only when semantic fields yield no usable
terms. A query with no search hits does not switch to identity. Existing
neighbor, root and recent-page merging, scope isolation and page limits remain
in effect. There is one generic multilingual path; inflection and paraphrase
recall are not guaranteed by this lexical policy. When enabled, embeddings use
the bounded original semantic fields independently of lexical tokens, with
identity fallback only when semantic text is empty.

### Optional hybrid retrieval

`knowl.embeddings` selects a separate OpenAI-compatible embedding API or the
inert default. The maintainer provider remains independent. Both SQLite and
PostgreSQL use the same semantic preparation, max-cosine page scoring and
reciprocal rank fusion with their existing lexical candidate order. There is no
language-specific route, in-process model or ANN extension.

Semantic inputs preserve original case and use NFC with normalized line endings.
Fixed 384-rune chunks overlap by 64 runes; pages retain at most 16 chunks and
queries or source signals at most four. Omitted coverage is reported. Model
prefixes are applied once. Private lexical tokens, paths and provenance do not
enter the model. Complete source and factual-page authority in maintenance stays
unchanged; semantic chunks are only a retrieval projection.

Network inference runs outside SQL locks and transactions. A rebuild commits
lexical state first, invalidates dense readiness, then publishes the complete
bounded vector projection only if its canonical snapshot still matches. SQLite
stores little-endian float32 vectors in BLOBs; PostgreSQL uses BYTEA and the same
scoped advisory lock for lexical replacement and dense publication. Migration
16 adds these disposable projections and durable retrieval reports, without
changing canonical Markdown or raw evidence.

Enabled reads verify the complete projection, lexical candidates, filters and
original references in one consistent read transaction. Each page contributes
its maximum cosine across query windows and page chunks. Each channel keeps
`min(100, max(20, 4*k))` candidates; equal-weight RRF uses ranks starting at one
and constant 60, deduplicating pages per channel and breaking ties by page ID.
The fused relevance seeds feed the existing neighbor/root/recent context policy.
Lexical-only mode retains native ordering. Results retain original evidence and
citations; semantic similarity does not establish factual agreement.

Projection capacity is 8,192 chunks and 64 MiB per scope. Incompatible, incomplete
or over-capacity dense state fails as a whole. `failure_policy: lexical` returns
explicit degraded lexical results for classified embedding failures; `strict`
returns a typed failure. Invalid input and caller cancellation never become
successful fallback. Queries do not rebuild the projection. Startup retries a
current degraded projection once; operators can restart after repairing the
service or invoke the embedded `RebuildProjection` function.

Go Query/Ingest results expose a per-call `RetrievalReport`. Maintenance persists
a bounded report before maintainer inference, including failed selection, under
the current operation attempt; historical reports retain their attempt origin.
HTTP/MCP expose only optional `retrieval.effective` and safe `retrieval.reason`.
Expanded plans and selected-context diagnostics remain separate work.

A model-space fingerprint binds model, immutable revision, dimensions, prefixes,
preprocessing and normalization. Source-maintenance-v6 additionally binds fusion,
candidate/capacity and failure policy. Addresses, credentials and CPU settings do
not identify the output policy. Changing the space requires a complete rebuild.
Genuine historical staged recovery publishes lexical state without new
inference; dense repair runs separately. Old unplanned generations reject before
embedding or maintainer calls. See [configuration and recovery](operations.md#optional-embeddings)
and the [real CPU quality measurements](testing.md#real-cpu-embedding-quality-gate).

### Source maintenance contract v6

`MaintenanceInput.contract_version` is `source-maintenance-v6`. Its `pages`
contain selected ordinary factual snapshots. Its `catalogs` contain the complete
bounded navigation graph as `HierarchyCatalog` values: canonical `path`,
original `digest`, `title`, and sorted `children`. The root comes first;
remaining catalogs are path-sorted. Full catalog Markdown stays in the
application and is excluded from provider input. Catalog bounds are independent
of the factual `ReadLimits.Pages` default of 20.

Source plans add memberships through `ModelEditPlan.CatalogAdditions`
(`catalog_additions` in provider JSON). Existing catalogs require their original
`expected_digest` and omit `title`. New catalogs require an empty digest, a safe
nonempty single-line title and children. Children are canonical wiki file paths
that already exist or are created in the same plan. Duplicate memberships are
no-ops; duplicate entries for one catalog are invalid.

The application appends escaped links to authoritative catalog originals,
preserving unrelated prose and existing links. It validates the final graph and
the combined factual/catalog file mutations before staging. Arbitrary model
`index.md` FileEdits are rejected, including through the supplied `FilePlan`
seam. New catalogs and edited factual pages must be reachable from the root;
cycles, invalid targets, bounds and stale digest failures cannot commit.
Explicit hierarchy reconciliation retains its separate complete-graph contract
and can restructure navigation; it does not accept `catalog_additions`.

Source request assembly measures the exact serialized envelope and, for the
built-in runtime, the complete current structured user prompt: instructions,
input/output schemas and framing in UTF-8 bytes. The shared wire encoder sends
complete factual `Content` once and omits `Body`; application snapshots retain
both fields. Its default and supported local ceiling is 4,194,304 bytes. The
effective cap is the smaller application/provider declaration, captured with a
non-secret format identity before operation reservation.

Complete source text, schema and the bounded catalog graph are indispensable.
If they alone exceed the cap, `input_budget/required_input_limit` fails before
inference or staging while immutable raw remains accepted. Otherwise the app
reads first-seen ordinary candidates in existing priority order, one complete
page at a time under the original per-page/count/deadline bounds. Controls and
duplicates do not consume the factual allowance. A page that does not fit is
omitted; later smaller pages can still fit. Editable pages are never excerpted.
Existing factual edits must target an included full snapshot and copy its exact
digest, including supplied `FilePlan` calls. Canonical commit preconditions
still protect against later changes.

`MaintenanceRequestSizer` lets embedded maintainers declare a pure capacity,
format identity and exact request size. A plain custom maintainer without that
port assumes the shared JSON envelope only; a custom wrapper must implement
sizing for its complete request. The built-in runtime also checks the actual
current prompt before the inner agent runs, preventing overflow if SDK framing
drifts. This contract excludes token capacity, remote HTTP serialization,
session history and total workspace RAM. `IngestResult.Budget` is transient typed
byte/count evidence, not a public durable HTTP/MCP diagnostics contract.

The contract version, schema digest, effective request cap/format identity and
read, plan and catalog limits, plus the configured output-affecting retrieval
policy, participate in the maintenance-policy generation. Incompatible unplanned work
fails before inference with `maintenance_policy_mismatch`. Already validated
v1/v2/v3/v4/v5 concrete stages resume through canonical preconditions without
embedding or maintainer inference;
terminal operations remain replayable. See [operator bounds and upgrade
recovery](operations.md#source-maintenance-context-and-navigation).

OKF Attested Computation declarations are data, not an execution interface.
Knowl preserves and exposes their runtime, parameters, computation, executor,
and attester fields without loading resources or running any declared program.

## Supported usage surfaces

```text
Agent-facing data plane:     MCP
Deterministic host control:  HTTP/OpenAPI
Go in-process alternative:   Fx over the same runtime
Operator convenience:        cmd/knowl
Underlying business policy:  pkg/knowl/app
```

The supported Go imports are:

- `pkg/knowl` for plain-Go host composition;
- `pkg/knowlfx` for Fx lifecycle integration;
- `pkg/knowl/mcp` for the bounded MCP adapter;
- `pkg/knowl/types` for transport-neutral domain types when embedding needs
  them.

Everything else is implementation detail of a surface, not another product
API. For example, a Balda host connects through its external MCP configuration
and does not embed, start, configure, or persist Knowl itself.

## Architecture and ownership

Dependencies flow inward:

```text
entrypoints and surfaces -> composition -> adapters -> app policy -> shared types
```

Current ownership follows that direction:

```text
pkg/knowl/types       shared IDs and data shapes
pkg/knowl/wiki        wiki and frontmatter semantics
pkg/knowl/app         business policy and consuming ports
content/fs, store/*, provider
                       adapters for workspace, operational state, and provider
pkg/knowl/mcp         MCP adapter
internal/httpapi      HTTP adapter
internal/mcphttp      Streamable HTTP transport for MCP
internal/source       filesystem adapter, normalization, and reconciliation
pkg/knowl             composition root and host API
pkg/knowlfx, cmd/knowl
                       Fx and CLI entrypoints
```

`pkg/knowl/types` has no Knowl-package dependencies. `pkg/knowl/app` owns
business policy and ports; adapters depend on it, not the reverse.
`pkg/knowl` is the only package that composes multiple adapters. Fx and the
CLI must not become second composition roots with separate business rules.

Bootstrap is deliberately only a CLI preflight: it checks freshness and path
separation, initializes the local workspace/config, constructs one deterministic
filesystem source, and calls `Host.SyncSource` once. Bootstrap remains optional.
Ordinary sync has no freshness rule: it persists immutable raw evidence,
reserves idempotent maintenance work for changed text, and never copies source
content into `wiki/`. The sequential operation scheduler asks the maintainer to
create or update root-reachable semantic entities, concepts, and syntheses.
Sidecar and embedded callers use this same Host engine and durable recovery
order.

Every curated factual page cites accepted raw refs. A page may combine refs from
multiple configured sources; projection resolves them into a sorted
`source_documents` collection, and filtering matches any contributing source.
Deleting an upstream document tombstones active source state but does not
implicitly erase accumulated knowledge. The next successful sync also removes
that source's legacy derived `wiki/sources/<source_id>/**` subtree using staged
recovery while preserving raw revisions and curated pages.

Canonical-format migration is similarly explicit: `knowl migrate okf-v0.2`
preflights and journals the conversion, preserves the exact legacy log in an
archive, commits a marker last, and rebuilds projections. Host startup and
read-only commands reject legacy canonical state rather than changing it.

When code becomes difficult to read, split files within its package first.
Create a package only for a distinct usage surface, external technology
adapter, or shared semantic contract—not to shorten a file or prepare
hypothetical reuse. `.go-arch-lint.yml` protects these top-level boundaries.

## Invariants and non-goals

- Provenance is durable and inspectable.
- Local defaults are bounded and deterministic.
- Startup validates the workspace, initializes storage, performs recovery, and
  prepares projections before readiness.
- Canonical writes preserve one-writer ordering.
- Knowl does not promise automatic crawling or research, vector DB as
  canonical storage, implicit forgetting, binary/image understanding, Git
  push/sync, a broad CRUD/admin API, or shared multi-tenant security.
