# Context and maintenance baseline

The `context-baseline-v1` fixtures measure real retrieval, ingest, provider input
and scheduler behavior before the improvements in Epic `knowl-wxe`. They use
temporary workspaces and controlled inference dependencies. Default runs need
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
| `generic-title`, `frontmatter`, `fenced-heading` | `TestContextBaselineSourceSignals`: actual app ingest, content and SQLite; old relevant page plus 30 newer decoys | REQ-SOURCE-001; `.3` |
| `frontmatter-title`, `fenced-heading-title` | `TestContextBaselineSourceTitles`: actual title extraction | REQ-SOURCE-001; `.3` |
| `catalog-scaling` | `TestContextBaselineCatalogScaling`: 32 actual catalogs including root, default page bound 20 | REQ-CONTEXT-001; `.2` |
| `aggregate-input`, `serialized-envelope` | `TestContextBaselineProviderInputBudget`: real filesystem reads and RuntimeMaintainer | REQ-BUDGET-001; `.4` |
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

## Initial observations

On the initial implementation, exact/base-form/mixed queries retrieve their
expected pages. The two inflected Russian queries, English plural and semantic
paraphrase miss. Generic titles, frontmatter and fenced headings omit the old
relevant page from ingest context. Title extraction returns `---` for frontmatter
and `Decoy` for the fenced heading. The 32-catalog ingest returns
`app.ErrPlanLimitExceeded` before inference.

Four individually readable UTF-8 pages produce 4,458,791 bytes of serialized
input, exceeding the default 4,194,304-byte provider limit. The provider safely
rejects them with `provider_input_limit` before inference. A separate case sets
the payload budget exactly to 1,114,991 bytes: the accepted envelope is
1,115,109 bytes and the complete structured wrapper prompt is 1,122,363 bytes.
These are measured bytes, including escaping, base64 and duplicated page fields;
they are not token estimates. Full wire input must remain intact.

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
