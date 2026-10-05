# Context and maintenance baseline

The `context-baseline-v1` fixtures measure real retrieval, ingest, provider input
and scheduler behavior throughout Epic `knowl-wxe`. Improved scenarios become
strict regression gates while unrepaired quality cases remain observations.
They use temporary workspaces and controlled inference dependencies. Default runs need
no model service, runtime URL downloads or containers. A cold Go cache may
download the declared toolchain and dependencies.

Run the baseline with the Go version in `go.mod`:

```sh
go test -count=1 -json ./pkg/knowl/... \
  -run 'TestContextBaseline|TestIngestRejectsStaleReviewedPlan' \
  > /tmp/knowl-context-baseline.jsonl
```

Native Go test JSON events contain named test outcomes and JSON case observations
in their `Output` fields. There is no separate collector or report command.
Use `-v` instead of `-json` to read results directly, or `-count=2` to repeat the
run. Compare decoded case observations, not event timestamps or test durations.
Retrieval, source-context and execution cases also compare two controlled passes
inside the test. File modification times are fixed where they affect selection
or serialized byte counts.

Retrieval observations contain `case_id`, independently curated `expected` IDs,
actual ordered `observed` IDs, `k`, `hits`, `total`, `recall` and `outcome`.
Recall counts unique relevant IDs within the first k results. `met` means the
curated expectation was reached; `gap` records a known quality limitation.
Existing exact-match controls remain mandatory. Unexpected errors, malformed
evidence, lost provenance, nondeterministic replay, oversized accepted payloads
and unsafe writes fail tests. A repaired quality gap may become `met`; tests do
not require defects to persist. Later Stories promote their improved scenarios
to strict gates.

## Cases and ownership

| Cases | Real interface / existing control | Epic requirement; follow-up Story |
|---|---|---|
| `exact-body`, `word-form-base`, `mixed-language`, `generic-cyrillic-case`, `canonical-accent` | Shared `RunContextBaseline`; mandatory working retrieval controls | REQ-RECALL-001; `.5` |
| `word-form-genitive`, `word-form-instrumental`, `word-form-english`, `semantic-duplicate` | Same store Search interface and corpus, with expected canonical IDs | REQ-RECALL/EMBED-001; `.5`, `.10` |
| `generic-title`, `frontmatter`, `fenced-heading` | `TestContextBaselineSourceSignals`: actual app ingest, content and SQLite; old relevant page plus 30 newer decoys; strict recall gate | REQ-SOURCE-001; `.3` |
| `frontmatter-title`, `fenced-heading-title` | `TestContextBaselineSourceTitles`: actual shared source extraction; strict title gate | REQ-SOURCE-001; `.3` |
| `catalog-scaling` | `TestContextBaselineCatalogScaling`: 32 actual catalogs including root, factual page bound 20; strict ingest and commit gate | REQ-CONTEXT-001; `.2` |
| `aggregate-input`, `serialized-envelope`, `application-input-fitting` | `TestContextBaselineProviderInputBudget`: real filesystem reads, application fitting and actual RuntimeMaintainer SDK prompt; strict gates | REQ-BUDGET-001; `.4` |
| `uri-reference-http`, `uri-reference-mcp` | Actual Host HTTP/MCP ingest; raw/provider reference equality and local HTTP fetch counter | REQ-URI-001; `.6` |
| `http-content-origin`, `mcp-content-origin` | `TestContextBaselineURIReferenceThroughHTTPAndMCP`: supplied body, default media type, source identity, terminal replay and zero URL fetches | REQ-URI-001; `.6` |
| Missing or conflicting ingest payloads | `TestPublicIngestRequiresExactlyOnePayload`: real HTTP machine-readable `invalid_request` and MCP typed `ErrInvalidArguments` | REQ-URI-001; `.6` |
| Bounded structured output correction | `TestRuntimeCorrectionSequence` and actual runtime/application fixtures; strict gates | REQ-OUTPUT-001; `.8` |
| `blocked-execution` | Actual scheduler cycle, existing controlled runner/claim seams and channel barriers | REQ-EXEC-001; `.9` |
| `stale-write` | Existing `TestIngestRejectsStaleReviewedPlan`: real precondition rejection and preserved human edit | REQ-EXEC-001; `.9` |
| Cancellation, shutdown, atomic recovery | Existing scheduler renewal-loss/shutdown tests and filesystem recovery suites | REQ-EXEC/EVAL-001; `.9` |
| Knowledge loop, idempotency, provenance, replay, restart | Existing `TestHostGoldenKnowledgeLoopThroughMCPAndHTTP`, `TestHostGoldenAcceptedOperationResumesAcrossRestart`, store golden projection contracts | REQ-EVAL-001; all Stories |

The original golden corpus and its 11/12 threshold are unchanged. New fixtures
have their own expectations. Russian word forms, English plurals and mixed text
use one generic retrieval path. Controlled inference and fake vectors cannot
prove embedding quality. The [real CPU quality gate](#real-cpu-embedding-quality-gate)
evaluates the selected self-hosted multilingual model.

## Baseline observations and strict gates

On the initial implementation, exact/base-form/mixed queries retrieve their
expected pages. The two inflected Russian queries, English plural and semantic
paraphrase miss. Before Story `.3`, generic titles, frontmatter and fenced
headings omitted the old relevant page from ingest context; title extraction
returned `---` and `Decoy`. All three source-context cases and both title cases
now require success through actual source parsing and ingest. Shared SQLite/
PostgreSQL contracts also require body/tag/heading evidence to retrieve the
expected page, exclude identity decoys from nonempty semantic queries, and use
identity only for empty queries. Parser adversarial syntax, exact/overflow
bounds, UTF-8, cancellation, invalid-source durable failure and v1/v2 queued/
staged compatibility have behavioral gates. Before Story `.2`, the 32-catalog
ingest returned `app.ErrPlanLimitExceeded` before inference. It now succeeds through
commit with all 32 compact nodes and exact expected root destinations. A
mixed-context regression also selects root/nested index, log and an ordinary
page: only the ordinary page reaches factual reads and provider input.
Independent catalog ceilings, original Markdown preservation, nested reachability, escaping,
no-op additions, raw catalog-edit rejection, final graph/combined plan bounds,
stale preconditions and v1 queued/staged compatibility have behavioral gates.

The original four-page UTF-8 fixture now fits: the shared Content-only wire
was measured at 2,230,845 bytes and the complete v5 SDK prompt at 2,239,474 bytes, below
4,194,304. This is a strict success gate with exact measured/actual equality and
typed envelope/provenance comparison. Setting the provider cap to the single-page
envelope size, 558,270 bytes, must reject before runtime creation or inference
because the full wrapper costs more.

A separate real application/SQLite/filesystem/runtime fixture uses eight large
individually readable pages plus a later small page. It retains the first seven
large pages, omits the eighth, then includes the complete small page. Its actual
SDK request is 3,915,441 bytes; the typed report must match that size with eight
included and one omitted candidate. These are fixed-fixture measurements, not
production capacity or token estimates. All source/schema/catalog input and each
included page remain complete.

Exact/one-byte-over source-request and actual SDK boundaries are strict gates,
including a deliberately mismatched wrapper-sizing guard. Tests exercise JSON
escaping, UTF-8, schema base64, long identity, cancellation, unsafe extension
marshalers, separate impossible source/schema/catalog input, unchanged raw and
no stage/canonical changes on failure. Controls/duplicate IDs do not consume the
factual allowance; omissions still consume the unique candidate count bound.
Real FilePlan coverage rejects omitted existing edits even with the correct
digest, preserves unrelated prose and old/new citations on complete updates, and
retains stale human-write protection. Typed cap/format changes discriminate
policy generations before reservation. Frozen historical policy payloads from
v1 (`9e9edf0`), v2 (`fddb1a3`) and v3 (`0a1d133`) and v4 (`c929d69`) drive queued rejection and
concrete-stage/terminal replay recovery without new inference.

Current default scheduler order is `first_started`, `first_released`,
`second_started`. This records default serial execution; it does not establish
future configured capacity-two behavior. Story `.9` owns that configuration and
its strict progress/isolated-session test. URI references and caller-supplied
content with a URL origin preserve raw/provider content, default media types
and source identity through both public transports. Terminal idempotent replay
returns the same completed operation without new inference or a URL fetch.
Missing and conflicting payloads fail through the transports' stable error
contracts. Stale-write safeguards also pass their existing contracts. These fixtures measure specific
limitations, not universal semantic recall or duplicate prevention.

## Generic lexical retrieval gates

Story `.5` uses one shared `searchtest`/`contexttest` corpus through actual SQLite
and integration-tagged PostgreSQL. Independently authored originals and expected
IDs cover canonical accents, ordinary English/Russian lowercase, mixed technical
words, accent/whole-word negatives and literal question words. A two-rune excerpt
must return the original `e` plus combining accent. Original titles, provenance,
OKF fields, strict-then-relaxed ordering, field priority, path ties, scope/source
filters, cancellation and deterministic rebuild remain strict controls. Numeric
native scores and arbitrary cross-backend rank equality are not assertions.

Exact/overflow tests exercise raw query/source limits, term counts/runes,
256-entry/16-unique filters, direct snippet defaults/clamping and projection
ceilings. Real PostgreSQL indexes/searches the maximum 1,640-byte private term,
8,192 distinct words and an exact 524,288-byte page stream; typed SQL measures
native vector size. One-byte derived overflow and failed/canceled rebuilds retain
the prior projection. Historical migration 14 to 15, reopen/rebuild and Down
invalidate incompatible tokens/readiness while preserving original and durable
operation/source/document state. A real filesystem/SQLite `QueryService` gate
preserves original evidence and citations without writing or inference.

Two fresh native baseline runs produce identical nine decoded observations.
`generic-cyrillic-case` and `canonical-accent` are strict `met` controls; inflected
Russian words, the English plural and semantic paraphrase remain `gap` in this
lexical implementation. They may improve without breaking tests. These results
measure this fixed lexical corpus. The optional semantic profile is measured
separately below. The existing golden 11/12 gate stays unchanged. Genuine v1-v5
policy fixtures gate old queued rejection and concrete stage/terminal replay
without embedding or maintainer inference. Complete request sizing and
whole-page authority guards remain enforced under contract v6.

## Real CPU embedding quality gate

The integration-tagged `pkg/knowl/store/eval` runner calls the actual pinned
TEI/E5-base API and exercises real SQLite and PostgreSQL. Its corpus was reviewed
before model results: all nine baseline cases, two independently authored
English/Russian positives with zero overlap under the actual lexical normalizer,
exact-title and mixed-token controls, and 22 technical distractors including
negation. Expected IDs and k remain fixed. Fake vectors and skipped integration
tests are not model-quality proof.

Every positive must appear in Query top five and the first five direct source
relevance seeds (`SelectContext` with eight pages, before neighbor/root/recent
fallback). Unique exact title must rank first; relevant evidence must outrank
each designated distractor. Both stores repeat the corpus twice, comparing
ordered IDs and original evidence/citations, filters, updates, deletion, foreign
scope and cancellation. The separate unchanged golden corpus requires at least
11/12. Existing native/backend tests cover protocol bounds, vector corruption,
capacity, atomic publication, reports, generations and historical recovery.

Recorded results from the same corpus:

| Profile | Query/source top-five cases | Golden | Negated Russian rollout control |
| --- | --- | --- | --- |
| E5-small / 384 dimensions | 13/13 | 12/12 | Failed: rollback rank 2, distractor rank 1 |
| E5-base / 768 dimensions | 13/13 | 12/12 | Passed: rollback rank 1, distractor rank 2 |

E5-base passes all gates on both stores in both repeats. The
[passing artifact](../pkg/knowl/store/eval/testdata/cpu-e5-base.json) records the
exact image/model revision, immutable corpus hash, per-case lexical/hybrid/source
ranks and cosines, request/rebuild timing, allocation, machine and memory data.
The [failed small-model artifact](../pkg/knowl/store/eval/testdata/cpu-e5-small.json)
is retained. The model was changed with explicit approval; the corpus, k,
expected IDs and algorithm were preserved. These measurements do not establish
universal recall, contradiction detection or semantic duplicate prevention.

The selected reference is `intfloat/multilingual-e5-base`, revision
`d128750597153bb5987e10b1c3493a34e5a4502a`, mean float32/normalized 768 dimensions,
CPU TEI 1.9.0 at the digest in the Compose overlay. Service allocation was 2 CPU
and 4 GiB; the daemon was x86_64 with four CPUs and 33,657,823,232 bytes RAM.
Observed cold readiness was 194.5 seconds, cached restart 6.6 seconds, base model
cache 1,075 MiB and idle memory about 1.91 GiB. Network-disabled cache startup,
768-vector inference, token overflow/alias errors, restart equality and real
fallback/strict outage behavior were also verified. Inspect per-run timings in
the artifact rather than treating these values as deployment guarantees.

For an already reachable selected reference service and disposable PostgreSQL
fixture, run:

```bash
export KNOWL_EMBEDDING_EVAL_ENDPOINT='http://your-tei:80/v1/embeddings'
export KNOWL_EMBEDDING_EVAL_POSTGRES_DSN='postgres://fixture-user:fixture-password@your-postgres/fixture-db?sslmode=disable'
export KNOWL_EMBEDDING_EVAL_OUTPUT='/tmp/knowl-embedding-quality.json'
go test -tags integration -count=1 -v ./pkg/knowl/store/eval \
  -run 'TestQualityCorpus|TestRealCPUModel'
```

The reference gate verifies TEI `/info` identity and settings as well as vectors;
this is separate from Knowl's generic embedding API protocol. An absent endpoint
explicitly skips local model tests. A configured endpoint requires the PostgreSQL
fixture. Skipping is never recorded as a passing model gate.

The Compose profile exposes no TEI host port. To reproduce on its private network,
start only TEI from this checkout and create a disposable database:

```bash
docker compose -p knowl-evaluation -f deploy/sidecar/compose.yaml \
  -f deploy/sidecar/embeddings.compose.yaml up -d tei
# Wait until `docker compose ... ps tei` reports healthy.
docker run -d --name knowl-eval-postgres \
  --network knowl-evaluation_default --network-alias postgres-eval \
  -e POSTGRES_USER=knowl_eval -e POSTGRES_PASSWORD=knowl_eval_fixture \
  -e POSTGRES_DB=knowl_eval postgres:16-alpine
docker exec knowl-eval-postgres pg_isready -U knowl_eval -d knowl_eval

CGO_ENABLED=0 go test -c -tags integration \
  -o /tmp/knowl-embedding-eval.test ./pkg/knowl/store/eval
mkdir -p .artifacts/embeddings
TEI_IMAGE='ghcr.io/huggingface/text-embeddings-inference:cpu-1.9.0@sha256:bc7ad262695df5b7875b0c9c702deb8e9df3953bdf22c3d6068d9c0429b7b3f3'
docker run --rm --network knowl-evaluation_default \
  --user "$(id -u):$(id -g)" \
  -v /tmp/knowl-embedding-eval.test:/usr/local/bin/embedding-eval.test:ro \
  -v "$PWD/.artifacts/embeddings:/results" \
  -e KNOWL_EMBEDDING_EVAL_ENDPOINT=http://tei:80/v1/embeddings \
  -e KNOWL_EMBEDDING_EVAL_POSTGRES_DSN='postgres://knowl_eval:knowl_eval_fixture@postgres-eval:5432/knowl_eval?sslmode=disable' \
  -e KNOWL_EMBEDDING_EVAL_OUTPUT=/results/evaluation.json \
  --entrypoint /usr/local/bin/embedding-eval.test "$TEI_IMAGE" \
  -test.run 'TestQualityCorpus|TestRealCPUModel' -test.v -test.timeout 10m
```

The runner writes bounded safe numeric/identity results. For a comparable recorded
artifact, add the actual image, machine, CPU/RAM allocation, memory/cache and
cold/warm startup measurements; the two checked-in records include these
observations. Clean up the fixture while preserving the model cache:

```bash
docker rm -f knowl-eval-postgres
docker compose -p knowl-evaluation -f deploy/sidecar/compose.yaml \
  -f deploy/sidecar/embeddings.compose.yaml down
```

## Verification

Run all ordinary regression checks before pushing:

```sh
go test ./...
go tool golangci-lint run ./...
go run github.com/fe3dback/go-arch-lint@v1.15.0 check --project-path .
```

PostgreSQL uses the same retrieval cases through the existing store contract.
Its container-backed coverage stays behind the integration build tag and needs
a working container runtime:

```sh
go test -tags integration -count=1 ./pkg/knowl/store/postgres \
  -run TestStoreContractWithTestcontainers
```

For the shared corpus and migration failures under race detection:

```sh
go test -race -count=1 ./pkg/knowl/store/sqlite \
  -run 'TestSearchContract|TestContextContract|TestContextBaseline|TestSQLiteGeneric'
go test -race -tags integration -count=1 ./pkg/knowl/store/postgres \
  -run TestStoreContractWithTestcontainers
```


## Durable operation diagnostics gates

The app fixtures capture the actual maintainer input and serialized request
size, including whole-page budget exclusions, exact and unknown required overflow,
partial reads/cancellation, a failed provider, and a refused report write.
A cancellation after successful fitting must retain `assembled` evidence with
zero inference. Twenty long but valid candidate IDs force report-list truncation
while preserving every actual model page and the complete fitting counts.
Strict projection failures retain the original check without invented assembly.

Shared SQLite and integration-tagged PostgreSQL contracts cover immutable
per-attempt snapshots, current/older attempts, scope, terminal guards, identical
repeats, concurrent conflicting writes, reopen, work claims, malformed/oversized
stored values and migration 17 up/down. Known zero staged file counts and unknown
legacy counts remain distinct; opaque legacy digests stay readable but private.
Bounded allocation checks catch loading oversized new report/digest columns.
Missing or null required report counters/attempt fields must fail with the
invalid-report sentinel through codec and both stores, preserving explicit zero.
Actual HTTP/MCP operation reads reject these corrupt rows through their safe
error boundary instead of publishing invented zero-valued facts.

`TestOperationDetailsHTTPAndMCPDurableParity` uses real filesystem/SQLite ingest,
reopens the database and reads both generated HTTP responses and MCP tool results.
Queued, committed, provider-failed and required-overflow operations must agree on
context, retrieval, plan, warning and execution fields, including `failure.reason`.
The committed fixture also exercises application-invalid then valid output and
retains the correction report after database reopen. Generated HTTP models and
MCP preserve two physical turns versus one scheduler attempt. Custom fallback
omits physical measurements, preplanning/legacy facts remain absent, and both
ports reject missing/null required correction fields with safe errors.
Repeated polling and terminal replay cannot invoke inference or change canonical
pages. Distinct source, query, rationale and upstream-error sentinels are checked
in parsed public JSON values.

The bounded/historical port fixture exercises JSON-escaped candidate IDs, maximum
safe counters, truncation, a producing attempt older than the current work attempt,
legacy missing facts and private opaque digests. Its actual generated HTTP/MCP
serialization must preserve typed facts and keep details below 96 KiB. The index
rejects any search or selection during operation reads. These are interface and
storage checks; no documentation/source substring tests or new embedding-quality
experiment are required.

Run the repository's full native tests, race tests, lint and architecture checks,
repeat OpenAPI generation to confirm no drift, and run the tagged PostgreSQL
contracts before delivery. Closure requires all required checks to pass on the
exact PR head and all Story changes to merge.

## Bounded output correction gates

The ordinary offline test suite uses the pinned structured runner with controlled
agent output and real application/filesystem/SQLite boundaries. The table-driven
`TestRuntimeCorrectionSequence` evaluator emits decoded JSON observations with
case ID, measured report and `met` outcome after its assertions pass. All cases
are strict gates, rather than quality observations requiring a model service.

```sh
go test -count=1 -json ./pkg/knowl/... ./internal/httpapi/server \
  -run 'TestRuntimeCorrection|TestIngestCorrection|TestHierarchyCorrection|TestSourceCorrectionPolicy|TestIngestReservesFeedback|TestHostOutputConfiguration|TestPublicOperationCorrection|TestOperationDetails' \
  > /tmp/knowl-output-correction.jsonl
```

| Behavior | Gate |
| --- | --- |
| Valid first; malformed/schema/branch/application rejection then valid; zero; exhaustion; mixed layers | Runtime sequence evaluator with exact physical calls and one shared allowance. |
| Full validation before artifacts | Actual runner-to-app schema/provenance/mixed cases and source/hierarchy callback observers. Invalid intermediate candidates cannot stage or save a plan. |
| Aggregate output and safe feedback | Exact/overflow, partial/final and thought/error-event fixtures; parsed unchanged requests and allowlisted feedback codes with no private output/error text. |
| Total deadline and cancellation | Runtime wait/build/session and across-turn deadlines; caller cancellation before/between/after accepted output and report attribution. |
| Transport separation | Transport/late-error fixtures remain retryable with one generation; exhausted output is permanent. Scheduler counters retain their existing meaning. |
| Reserve and recovery | Complete-page exact-fit reserve versus actual usage; old unplanned descriptors reject policy changes while authenticated stages replay without inference. |
| Durable and public evidence | Shared SQLite/PostgreSQL contracts, restart/claim/current/historical/terminal corruption guards, generated HTTP/MCP parity and actual maximum combined details below 96 KiB. |

Planning fixtures make readiness explicit through real SQLite reservation, and
recovery fixtures mark leases explicitly expired through the real store API.
They do not rely on nanosecond delays or weaken canonical/idempotency assertions.
Run the full native/race/lint/architecture suites, repeat generation and tagged
PostgreSQL contracts before delivery. No runtime dependency upgrade or real
embedding experiment is needed for these output-policy changes.
