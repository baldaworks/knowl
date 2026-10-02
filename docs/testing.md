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
| `exact-body`, `word-form-base`, `mixed-language` | Shared `RunContextBaseline`; mandatory working retrieval controls | REQ-RECALL-001; `.5` |
| `word-form-genitive`, `word-form-instrumental`, `word-form-english`, `semantic-duplicate` | Same store Search interface and corpus, with expected canonical IDs | REQ-RECALL/EMBED-001; `.5`, `.10` |
| `generic-title`, `frontmatter`, `fenced-heading` | `TestContextBaselineSourceSignals`: actual app ingest, content and SQLite; old relevant page plus 30 newer decoys; strict recall gate | REQ-SOURCE-001; `.3` |
| `frontmatter-title`, `fenced-heading-title` | `TestContextBaselineSourceTitles`: actual shared source extraction; strict title gate | REQ-SOURCE-001; `.3` |
| `catalog-scaling` | `TestContextBaselineCatalogScaling`: 32 actual catalogs including root, factual page bound 20; strict ingest and commit gate | REQ-CONTEXT-001; `.2` |
| `aggregate-input`, `serialized-envelope`, `application-input-fitting` | `TestContextBaselineProviderInputBudget`: real filesystem reads, application fitting and actual RuntimeMaintainer SDK prompt; strict gates | REQ-BUDGET-001; `.4` |
| `uri-reference-http`, `uri-reference-mcp` | Actual Host HTTP/MCP ingest; raw/provider reference equality and local HTTP fetch counter | REQ-URI-001; `.6` |
| Invalid structured output | Existing `TestRuntimeMaintainerRejectsUnsafeOutputAndLimits`, with classified errors | REQ-OUTPUT-001; `.8` |
| `blocked-execution` | Actual scheduler cycle, existing controlled runner/claim seams and channel barriers | REQ-EXEC-001; `.9` |
| `stale-write` | Existing `TestIngestRejectsStaleReviewedPlan`: real precondition rejection and preserved human edit | REQ-EXEC-001; `.9` |
| Cancellation, shutdown, atomic recovery | Existing scheduler renewal-loss/shutdown tests and filesystem recovery suites | REQ-EXEC/EVAL-001; `.9` |
| Knowledge loop, idempotency, provenance, replay, restart | Existing `TestHostGoldenKnowledgeLoopThroughMCPAndHTTP`, `TestHostGoldenAcceptedOperationResumesAcrossRestart`, store golden projection contracts | REQ-EVAL-001; all Stories |

The original golden corpus and its 11/12 threshold are unchanged. New fixtures
have their own expectations. Russian word forms, English plurals and mixed text
use one generic retrieval path. Controlled inference and fake vectors cannot
prove embedding quality: Story `.10` requires evaluation with the selected real
self-hosted multilingual model.

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
is 2,230,845 bytes and the complete v4 SDK prompt is 2,239,474 bytes, below
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
v1 (`9e9edf0`), v2 (`fddb1a3`) and v3 (`0a1d133`) drive queued rejection and
concrete-stage/terminal replay recovery without new inference.

Current default scheduler order is `first_started`, `first_released`,
`second_started`. This records default serial execution; it does not establish
future configured capacity-two behavior. Story `.9` owns that configuration and
its strict progress/isolated-session test. URI references and stale-write
safeguards pass their existing contracts. These fixtures measure specific
limitations, not universal semantic recall or duplicate prevention.

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
