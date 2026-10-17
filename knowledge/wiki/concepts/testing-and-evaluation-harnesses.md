---
type: concept
title: Testing, Baseline Gates, and Evaluation Harnesses
knowl:
  id: concepts/testing-and-evaluation-harnesses
  source_refs:
    - wiki-filesystem:knowl-docs/testing.md@d0c8c9c0da60fe39b4bff2aba427dad30e9c63bd1962ce3fa8561fc2dc1b1df4
    - wiki-filesystem:knowl-docs/testing/native-vectors.md@62fb85104bd4fdaf9bd9783125ce5d47303a784697c97b0307c176cd96ee5ff8
---
# Testing, Baseline Gates, and Evaluation Harnesses

Knowl verifies retrieval, source context selection, request fitting, structured output correction, concurrent scheduler execution, gradual source retirement, and native vector search through deterministic offline baseline suites and opt-in container/model evaluation harnesses. See [Maintenance Contract and Execution](maintenance-contract-and-execution.md), [Retrieval and Hybrid Search Architecture](retrieval-and-search.md), and [Workspace Semantics and Source Lifecycle](workspace-and-source-lifecycle.md).

## Standard Pre-Push Verification Commands

Run all ordinary regression, lint, and architecture checks with the Go toolchain version declared in `go.mod`:

```sh
go test ./...
go tool golangci-lint run ./...
go run github.com/fe3dback/go-arch-lint@v1.15.0 check --project-path .
```

Container-backed PostgreSQL store contracts stay behind the `integration` build tag and require a container runtime:

```sh
go test -tags integration -count=1 ./pkg/knowl/store/postgres \
  -run TestStoreContractWithTestcontainers
```

Run shared search/context/migration contracts under race detection with:

```sh
go test -race -count=1 ./pkg/knowl/store/sqlite \
  -run 'TestSearchContract|TestContextContract|TestContextBaseline|TestSQLiteGeneric'
go test -race -tags integration -count=1 ./pkg/knowl/store/postgres \
  -run TestStoreContractWithTestcontainers
```

## Context and Maintenance Baseline (`context-baseline-v1`)

The `context-baseline-v1` fixtures measure retrieval, ingest, provider input sizing, and scheduler behavior across Epic `knowl-wxe` using temporary workspaces and controlled inference dependencies (requiring no external model service, runtime URL downloads, or containers for default runs):

```sh
go test -count=1 -json ./pkg/knowl/... \
  -run 'TestContextBaseline|TestIngestRejectsStaleReviewedPlan' \
  > /tmp/knowl-context-baseline.jsonl
```

- **Event Format and Determinism**: Native `go test -json` events emit named test outcomes and JSON case observations inside `Output` fields (no separate collector command is used; pass `-v` to read directly or `-count=2` to verify repeat determinism). Retrieval, source-context, and execution tests also compare two controlled passes internally with fixed file modification times. Compare decoded observations (`case_id`, curated `expected` IDs, ordered `observed` IDs, `k`, `hits`, `total`, `recall`, and `outcome`: `met` vs. known quality `gap`) rather than timestamps or durations.
- **Promotion to Strict Gates**: Repaired quality gaps become strict `met` regression gates as stories land, while the original golden corpus and its **11/12** threshold remain mandatory.

### Baseline Cases and Requirement Ownership

| Cases | Real Interface / Control | Requirement & Story |
| --- | --- | --- |
| `exact-body`, `word-form-base`, `mixed-language`, `generic-cyrillic-case`, `canonical-accent` | Shared `RunContextBaseline`; mandatory working lexical retrieval controls | `REQ-RECALL-001`; `.5` |
| `word-form-genitive`, `word-form-instrumental`, `word-form-english`, `semantic-duplicate` | Same store `Search` interface and corpus (`gap` in lexical-only mode; evaluated for hybrid search under the CPU embedding gate) | `REQ-RECALL/EMBED-001`; `.5`, `.10` |
| `generic-title`, `frontmatter`, `fenced-heading` | `TestContextBaselineSourceSignals`: app ingest, content, and SQLite with old relevant page + 30 newer decoys; strict recall gate | `REQ-SOURCE-001`; `.3` |
| `frontmatter-title`, `fenced-heading-title` | `TestContextBaselineSourceTitles`: shared source extraction; strict title gate (replacing earlier `---` and `Decoy` defects) | `REQ-SOURCE-001`; `.3` |
| `catalog-scaling` | `TestContextBaselineCatalogScaling`: 32 catalogs including root, factual page bound 20; strict ingest/commit gate (mixed-context test also verifies that only ordinary pages reach factual reads/provider input while root/nested index and log are excluded) | `REQ-CONTEXT-001`; `.2` |
| `aggregate-input`, `serialized-envelope`, `application-input-fitting` | `TestContextBaselineProviderInputBudget`: real filesystem reads, application fitting, and `RuntimeMaintainer` SDK prompt; strict gates | `REQ-BUDGET-001`; `.4` |
| `uri-reference-http`, `uri-reference-mcp`, `http-content-origin`, `mcp-content-origin` | `TestContextBaselineURIReferenceThroughHTTPAndMCP`: Host HTTP/MCP ingest, raw/provider equality, default media type, source identity, terminal replay, and zero URL fetches | `REQ-URI-001`; `.6` |
| Missing or conflicting ingest payloads | `TestPublicIngestRequiresExactlyOnePayload`: HTTP `invalid_request` and MCP `ErrInvalidArguments` | `REQ-URI-001`; `.6` |
| Bounded structured output correction | `TestRuntimeCorrectionSequence` and runtime/application fixtures; strict gates | `REQ-OUTPUT-001`; `.8` |
| `blocked-execution` | Configured scheduler at capacity `1` (`first_started`, `first_released`, `second_started`) and `2` (`first_started`, `second_started`, `first_released`) with channel barriers | `REQ-EXEC-001`; `.9` |
| `stale-write` | `TestIngestRejectsStaleReviewedPlan`: precondition rejection preserving human edits | `REQ-EXEC-001`; `.9` |
| Cancellation, shutdown, atomic recovery, golden loop | Scheduler renewal-loss/shutdown suites, `TestHostGoldenKnowledgeLoopThroughMCPAndHTTP`, `TestHostGoldenAcceptedOperationResumesAcrossRestart`, and store golden projection contracts | `REQ-EXEC/EVAL-001`; all Stories |

### Fixed Input-Budget and Historical Policy Fixtures

- **Four-Page UTF-8 Fixture**: Measures **2,230,845 bytes** on the shared Content-only wire and **2,239,474 bytes** for the complete v5 SDK prompt (below the `4,194,304`-byte cap). Setting the provider cap to the single-page envelope size (**558,270 bytes**) is strictly rejected before runtime creation or inference.
- **Eight-Large-Plus-One-Small Fitting Fixture**: Retains the first 7 large pages, omits the 8th large page, and includes the later small page (`8` included, `1` omitted), matching an exact **3,915,441-byte** SDK request. Controls and duplicate IDs do not consume the factual allowance, whereas omissions still consume the unique candidate count bound.
- **Historical Policy Replay**: Frozen policy payloads from `v1` (`9e9edf0`), `v2` (`fddb1a3`), `v3` (`0a1d133`), `v4` (`c929d69`), and `v1–v5` fixtures verify that old unplanned queued descriptors are rejected on policy changes while concrete stages and terminal replays recover without new embedding or maintainer inference.

## Generic Lexical Retrieval Gates

Story `.5` runs a shared `searchtest`/`contexttest` corpus across SQLite and integration-tagged PostgreSQL:

- Verifies canonical accents (including a two-rune excerpt returning original `e` + combining accent), ordinary English/Russian lowercase, mixed technical tokens, accent/whole-word negatives, and literal question words.
- Enforces exact/overflow bounds: raw query/source limits, term counts/runes, `256`-entry / `16`-unique source filters, snippet defaults/clamping, and projection ceilings (PostgreSQL indexes/searches the maximum **1,640-byte** private encoded term, **8,192** distinct words, and an exact **524,288-byte** page token stream).
- Exercises Migration `14 -> 15` up/down, reopen/rebuild, and one-byte derived overflow rollback (preserving prior projections on failed/canceled rebuilds).

## Real CPU Embedding Quality Gate (`pkg/knowl/store/eval`)

The integration-tagged `pkg/knowl/store/eval` runner evaluates the pinned CPU TEI `1.9.0` server and `intfloat/multilingual-e5-base` (`d128750597153bb5987e10b1c3493a34e5a4502a`, `768` dimensions) against real SQLite and PostgreSQL:

- **Corpus and Strict Criteria**: Evaluates all 9 baseline cases, 2 independently authored English/Russian positives with zero lexical-normalizer overlap, exact-title and mixed-token controls, and 22 technical distractors (including negation), plus the separate **11/12** golden corpus. Every positive must appear in `Query` top 5 and the first 5 direct `SelectContext` relevance seeds (with 8 pages, before neighbor/root/recent fallback); unique exact title must rank 1st; relevant evidence must outrank designated distractors across two repeated passes on both stores.
- **Recorded Model Comparison** (checked-in artifacts `pkg/knowl/store/eval/testdata/cpu-e5-base.json` and `cpu-e5-small.json`):
  | Profile | Query/Source Top-5 Cases | Golden | Negated Russian Rollout Control |
  | --- | --- | --- | --- |
  | `E5-small` / 384 dims | 13/13 | 12/12 | **Failed**: rollback rank 2, distractor rank 1 |
  | `E5-base` / 768 dims | 13/13 | 12/12 | **Passed**: rollback rank 1, distractor rank 2 |
  Reference environment observations (x86_64 host with 4 CPUs / `33,657,823,232` bytes RAM; TEI service allocated 2 CPUs / 4 GiB RAM): **194.5s** cold readiness, **6.6s** cached restart, **1,075 MiB** model cache, and **~1.91 GiB** idle memory.

### Complete-Page Coverage Evaluation (51-Page Private Wiki Snapshot)

Measured on an authorized 51-page technical wiki snapshot (page-digest set SHA-256 `ad2518dd403ce09f810085b78a1e3a745ba60e0c8989a8da0d9d1f8bf8f5654c`) comparing the earlier 16-chunk/page limit (**644 chunks**, omitting **192,332** normalized runes across 31 pages) against complete-page indexing (**1,272 chunks**, max **79 chunks** for one page, **0 omissions**, within the `8,192`-chunk limit):

| Measurement | First 16 Chunks/Page | Complete Pages |
| --- | ---: | ---: |
| Rebuild (seconds) | 89 | 177 |
| Exact-title top-1 | 51/51 | 51/51 |
| Existing 25 paraphrases (top-5) | 19/25 | 19/25 |
| Three head / middle / tail questions (top-5) | 2/3 · 2/3 · 3/3 | 2/3 · 3/3 · 3/3 |
| Three tail questions (top-1) | 1/3 | 2/3 |
| Paraphrase search p50 / p95 (ms) | 95 / 106 | 132 / 156 |

### Running the Real CPU Embedding Evaluation

Against an already reachable reference TEI endpoint and disposable PostgreSQL database (an unset endpoint skips local model tests; skipping is never recorded as a passing gate):

```bash
export KNOWL_EMBEDDING_EVAL_ENDPOINT='http://your-tei:80/v1/embeddings'
export KNOWL_EMBEDDING_EVAL_POSTGRES_DSN='postgres://fixture-user:fixture-password@your-postgres/fixture-db?sslmode=disable'
export KNOWL_EMBEDDING_EVAL_OUTPUT='/tmp/knowl-embedding-quality.json'
go test -tags integration -count=1 -v ./pkg/knowl/store/eval \
  -run 'TestQualityCorpus|TestRealCPUModel'
```

To reproduce on the private Compose network (where TEI publishes no host port), supply unused placeholder values for the Knowl-only variables when starting `tei`:

```bash
OPENAI_API_KEY=unused OPENAI_MODEL=unused KNOWL_OPERATOR_TOKEN=unused \
  docker compose -p knowl-evaluation -f deploy/sidecar/compose.yaml \
  -f deploy/sidecar/embeddings.compose.yaml up -d tei

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

Clean up while preserving `tei-model-cache` via `docker rm -f knowl-eval-postgres` and `docker compose -p knowl-evaluation ... down`.

## Gradual Source-Retirement, Operation Diagnostics, Output Correction, and Concurrency Gates

- **Gradual Source-Retirement Suite**: Tests independent filesystem/Git replacement, SQLite/PostgreSQL `active -> retiring -> deleted` CAS lifecycle, replacement readiness ordering, retiring-ref staging containment, old-only page/catalog-closure deletion, mixed-page recomputation from sorted active evidence, page-unit fault-injection recovery, independent per-page retry, zero-only finalization, and `plan.empty_safe_subset` diagnostics:
  ```sh
  go test -count=1 ./pkg/knowl/app ./pkg/knowl/content/fs \
    ./pkg/knowl/store/sqlite ./internal/source/reconcile \
    ./internal/httpapi/operatorapi ./internal/httpapi/server ./cmd/knowl
  go test -tags integration -count=1 ./pkg/knowl/store/postgres \
    -run 'TestPostgresSourceLifecycleContract|TestStoreContractWithTestcontainers'
  ```
- **Durable Operation Diagnostics Gates**: `TestOperationDetailsHTTPAndMCPDurableParity` and shared SQLite/PostgreSQL contracts verify Migration `17` and `18` up/down, immutable per-attempt snapshots, 20-candidate report-list truncation (preserving actual model pages and counts), corrupt/null counter rejection via the invalid-report sentinel, zero search/selection calls during operation reads, and HTTP/MCP parity below the **96 KiB** details cap.
- **Bounded Output Correction Gates**:
  ```sh
  go test -count=1 -json ./pkg/knowl/... ./internal/httpapi/server \
    -run 'TestRuntimeCorrection|TestIngestCorrection|TestHierarchyCorrection|TestSourceCorrectionPolicy|TestIngestReservesFeedback|TestHostOutputConfiguration|TestPublicOperationCorrection|TestOperationDetails' \
    > /tmp/knowl-output-correction.jsonl
  ```
- **Bounded Maintenance Execution (Concurrency) Gates**:
  ```sh
  go test -count=2 -json ./pkg/knowl ./pkg/knowl/app \
    -run 'TestContextBaselineBlockedExecution|TestHostComposesDistinctRuntimeSessions|TestConcurrentPreparedSourcePlansFailSafely|TestOverlappingSourceAndHierarchyPreserveFirstCommit' \
    > /tmp/knowl-concurrency-eval.jsonl
  ```
  Verifies `REQ-WORK-CAPACITY`, `REQ-WORK-ISOLATION`, `REQ-WORK-CLAIMS`, `REQ-WORK-LIFECYCLE`, `REQ-WORK-INTEGRITY`, and `REQ-WORK-COMPATIBILITY` (including two active owners with a third unclaimed, independent ADK sessions, and conflict preservation of the first commit without automatic replanning).

## Native Vector Comparison Harness and Recorded Benchmarks

- **`BenchmarkVectorRuntime`**: Measures the real `SearchWithReport` path (scalar readiness/snapshot/model admission, source filtering, RRF fusion, and consumed-evidence validation, plus one full `Rebuild` call as `PublicationNS` and one same-snapshot `Project` call as `ReuseNS` outside the warm-query timer).
- **`BenchmarkVectorKernel`**: Compares native scalar/page cosine SQL against an isolated full-vector-transfer + Go dot-product baseline on disposable tables (omitting production validation, caps, inference, fusion, and evidence).
- **Default vs. Lab Matrix**: Default fixtures use **128 chunks**, **8 dimensions**, and **1 or 4 query windows** (deterministic normalized `float32` vectors from seed `73` and prepared input hashes; zero external inference calls). Setting `KNOWL_VECTOR_BENCH_LAB=1` enumerates **8,192 / 25,000 / 100,000 chunks**, **2 / 768 / 4,096 dimensions**, **1 / 4 windows**, **50% source eligibility**, plus **8,192 × 768 at 100%** under a **180-second** per-case context budget (reporting `projection_capacity` above production caps and `resource_limited` above **512 MiB** estimated kernel memory).
- **Before/After Historical Comparison**: Create a detached worktree at `dec6249b68a27435217e29bef9a5669fee068cb0`, copy the identical `internal/searchtest/{vector_benchmark,native_oracle}.go` and backend `vector_runtime_benchmark*_test.go` files without modifying production code, and run with `KNOWL_VECTOR_BENCH_REVISION` set:
  ```sh
  go test -v -json ./pkg/knowl/store/sqlite -run '^$' \
    -bench '^BenchmarkVectorRuntime' -benchtime=5x -benchmem -count=1
  go test -v -json -tags integration ./pkg/knowl/store/postgres -run '^$' \
    -bench '^BenchmarkVectorRuntime' -benchtime=5x -benchmem -count=1
  go test -v -json ./pkg/knowl/store/sqlite -run '^$' \
    -bench '^BenchmarkVectorKernel' -benchtime=5x -benchmem -count=1
  ```
  PostgreSQL plans parse numerical executor work from `EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON)`, whereas SQLite records structured `EXPLAIN QUERY PLAN`.

### Trusted Publication Comparison (2026-10-17, Revision `643933d61e82ff5d396ae5f3db3a13d57ba1c840`)

Measured sequentially on a single host (`Go 1.26.9`, `Linux 6.18.26.1-microsoft-standard-WSL2`, `amd64`, 4vCPU `AMD Ryzen 7 PRO 7840U`, `33,657,823,232` bytes RAM, no swap; `SQLite 3.53.3` / `sqlite-vec 0.1.9` with WAL and `busy_timeout 5000ms`; `PostgreSQL 16.14` / `pgvector 0.8.2` image `pgvector/pgvector:0.8.2-pg16@sha256:00ba258a66dac104fd5171074a0084462a64a1369d8513f3d0a634e2f24d15bc` with `shared_buffers 128MB`, `work_mem 4MB`, `jit=on`) across **8,192 chunks** distributed over **128 pages** (page limit `5`, dense candidate limit `20`, `n=3` warm queries per case; compatibility `P95NS` is nearest-rank max of 3), comparing legacy Go production (`dec6249b68a27435217e29bef9a5669fee068cb0`) against trusted native publication (`643933d61e82ff5d396ae5f3db3a13d57ba1c840`, recorded under `TrustRefinement` in `docs/testing/native-vectors.json` alongside dated historical `v1.3` medians):

| Backend | `dims/windows/source %` | Refreshed Go ms (min–max) | Trusted Native ms (min–max) | Historical v1.3 Native Median ms |
| --- | --- | ---: | ---: | ---: |
| `sqlite` | `2/1/50` | 67.593 (66.382–69.721) | 38.830 (38.455–39.545) | 1174.389 |
| `sqlite` | `2/4/50` | 69.634 (69.594–70.957) | 73.098 (69.270–73.840) | 1183.050 |
| `sqlite` | `768/1/50` | 426.163 (425.573–436.138) | 68.132 (64.332–69.368) | 2929.833 |
| `sqlite` | `768/4/50` | 452.554 (438.680–465.684) | 138.758 (137.375–156.208) | 3102.299 |
| `sqlite` | `768/1/100` | 462.949 (461.335–467.712) | 103.451 (101.413–112.053) | 3077.691 |
| `postgres` | `2/1/50` | 62.210 (60.539–62.291) | 168.249 (167.172–171.770) | 198.676 |
| `postgres` | `2/4/50` | 66.439 (63.545–67.300) | 176.600 (173.507–177.024) | 210.722 |
| `postgres` | `768/1/50` | 157.079 (155.512–158.211) | 186.948 (178.318–188.651) | 807.677 |
| `postgres` | `768/4/50` | 187.183 (184.219–194.752) | 214.689 (213.777–217.588) | 825.127 |
| `postgres` | `768/1/100` | 146.657 (142.780–147.521) | 59.517 (57.207–61.740) | 739.054 |

- **Transfer, Allocation, and Setup Metrics (`768/1/50`)**:
  - Every trusted native query transferred **2 scalar admission rows** (`276` logical result-cell bytes on SQLite, `259` on PostgreSQL), **0 coverage-manifest bytes**, and **0 stored vectors**, with **20 dense candidates**, **5 evidence rows**, and **32 total driver rows** including page fetches (`ScannedChunks=8192` from admitted published state). By contrast, the legacy Go pipeline transferred all **8,192 stored vectors** (`25,165,824` payload bytes / `24 MiB` per query at 768 dimensions).
  - Per-query Go allocation (`B/op`, not RSS) dropped from **79.904 MiB -> 1.684 MiB** on SQLite and **79.974 MiB -> 1.764 MiB** on PostgreSQL.
  - Mean scalar SQL row-consumption intervals were **0.132ms** (SQLite) and **1.101ms** (PostgreSQL), and ranking intervals were **61.867ms** (SQLite) and **173.686ms** (PostgreSQL).
  - Single-sample setup observations (`PublicationNS` / `ReuseNS`, outside query timers) measured **1.439s / 2.305s** on SQLite and **6.926s / 8.507s** on PostgreSQL.
  - Maximum `float64` true-cosine errors were **2.10326643e-7** (SQLite) and **9.97581830e-8** (PostgreSQL). A separate frozen-harness SQLite `8192×2/1/50` reproduction measured median **38.310ms** (range `36.936–38.727ms`, `n=3`, recorded under `TrustRefinement.Reproduction`).

### Historical v1.3 Full-Query Regression, Isolated Kernels, and Numerical Bounds (`6d4ee134fad6d19f6d4efbd3150d0b174f0e7cb4`)

Before the read-time trust refinement, the initial `v1.3` native implementation (`6d4ee134fad6d19f6d4efbd3150d0b174f0e7cb4` vs. legacy `dec6249b68a27435217e29bef9a5669fee068cb0`) audited full-scope coverage/vector state on every read and exhibited a material query latency regression (`8.02×` on SQLite `2929.833ms vs. 365.492ms` and `5.75×` on PostgreSQL `807.677ms vs. 140.520ms` at `8192×768/1/50`, with scalar row-consumption intervals of `2876.0ms` on SQLite and `706.7ms` on PostgreSQL, `1.74 MiB / 3,201` allocs on SQLite, `1.83 MiB / 3,203` allocs on PG, and `39` total driver rows):

- **Historical v1.3 Isolated Kernel Measurements** (`n=3`, disposable tables omitting production readiness, integrity, caps, inference, lexical search, fusion, and evidence; retained at `6d4ee134...`):
  | Backend | `chunks×dims` | `windows` | Legacy Median ms | Native Median ms |
  | --- | ---: | ---: | ---: | ---: |
  | `sqlite` | `25000×768` | 1 | 276.206 | 167.892 |
  | `sqlite` | `25000×768` | 4 | 236.812 | 356.603 |
  | `sqlite` | `100000×2` | 4 | 424.798 | 761.148 |
  | `sqlite` | `1024×4096` | 1 | 57.585 | 61.288 |
  | `postgres` | `25000×768` | 1 | 220.393 | 80.388 |
  | `postgres` | `25000×768` | 4 | 247.370 | 175.019 |
  | `postgres` | `100000×2` | 4 | 119.449 | 233.036 |
  | `postgres` | `1024×4096` | 1 | 45.388 | 6.200 |
- **Capacity and Memory-Guard Rejections**: On real backend publication APIs, `25000×2`, `100000×2`, and `8192×4096` returned typed `projection_capacity` (`0` samples). The isolated kernel pre-allocation guard (`512 MiB` working-set cap) rejected `8192×4096`, `25000×4096`, `100000×768`, and `100000×4096` as `resource_limited` (for example, `100000×4096` estimated `9,881,600,000` bytes vs. `536,870,912` allowed).
- **Numerical Bound and Accepted Ordering Differences**: Native samples verify true `float64` cosine within `epsilon(d) = 8*gamma(d+2) + 8*u` where `u = 2^-24` and `gamma(n) = n*u/(1-n*u)` (historical v1.3 max errors: `2.55914505e-7` SQLite, `1.17177782e-7` PG). Close near-match ordering legitimately differs across legacy near-unit `float64` dot product and backend `float32` native cosine (e.g., on `8192×2/4/50`: legacy `[p000086,p000072,p000114,p000006,p000090]`, SQLite native `[p000086,p000072,p000006,p000114,p000090]`, PostgreSQL native `[p000072,p000086,p000006,p000114,p000090]`).
