---
type: topic
title: Verification and Evaluation
knowl:
  id: concepts/verification-and-evaluation
  source_refs:
    - wiki-filesystem:knowl-docs/testing.md@d0c8c9c0da60fe39b4bff2aba427dad30e9c63bd1962ce3fa8561fc2dc1b1df4
    - wiki-filesystem:knowl-docs/testing/native-vectors.md@62fb85104bd4fdaf9bd9783125ce5d47303a784697c97b0307c176cd96ee5ff8
---
# Verification and Evaluation

Knowl verifies retrieval, maintenance, durable operations, recovery, and execution ownership through real application and storage interfaces. Controlled inference establishes execution and integrity behavior; actual embedding services are required to establish model quality. See [Architecture](architecture.md), [Content and Trust Boundaries](content-and-trust-boundaries.md), and [Service Operations](service-operations.md) for the contracts these evaluations exercise.

## Context Baseline and Result Interpretation

The `context-baseline-v1` fixtures measure retrieval, ingest, provider input, and scheduler behavior developed during Epic `knowl-wxe`. Improved scenarios become strict regression gates; unrepaired quality cases remain observations. Tests use temporary workspaces and controlled inference. Default runs require no model service, runtime URL downloads, or containers, although a cold Go cache may download the declared toolchain and dependencies.

Use the Go version declared in `go.mod`:

```sh
go test -count=1 -json ./pkg/knowl/... \
  -run 'TestContextBaseline|TestIngestRejectsStaleReviewedPlan' \
  > /tmp/knowl-context-baseline.jsonl
```

Native Go test JSON events carry named outcomes and JSON case observations in `Output`; there is no separate collector or reporting command. `-v` gives readable output and `-count=2` repeats execution. Compare decoded observations rather than event timestamps or durations. Retrieval, source-context, and execution cases also compare two controlled passes internally. Modification times are fixed when they affect selection or byte counts.

Retrieval observations contain `case_id`, independently curated `expected` IDs, ordered `observed` IDs, `k`, `hits`, `total`, `recall`, and `outcome`. Recall counts unique relevant IDs among the first k results. `met` reaches the curated expectation; `gap` records a known quality limitation. Improved gaps may become `met`; tests do not require defects to persist. Unexpected errors, malformed evidence, lost provenance, nondeterministic replay, oversized accepted payloads, and unsafe writes fail tests.

The original golden corpus retains its 11/12 threshold. New fixtures have independent expectations. Russian forms, English plurals, and mixed text use one generic retrieval path. Controlled inference and fake vectors do not prove embedding quality or universal duplicate prevention.

### Source Selection, Catalogs, and Request Budgets

Source-context cases `generic-title`, `frontmatter`, and `fenced-heading` exercise actual ingest, filesystem content, and SQLite with an old relevant page and 30 newer decoys. All three require recall success. `frontmatter-title` and `fenced-heading-title` require correct shared extraction, replacing historical results of `---` and `Decoy`.

Shared SQLite/PostgreSQL contracts require body, tag, and heading signals to find expected pages, exclude identity decoys for nonempty semantic queries, and use identity only for empty queries. Behavioral gates cover adversarial parsing, bounds, UTF-8, cancellation, invalid-source durable failure, and historical queued/staged compatibility.

`TestContextBaselineCatalogScaling` now commits successfully with 32 actual compact catalogs including the root and factual allowance 20; the earlier implementation failed with `app.ErrPlanLimitExceeded` before inference. A mixed-context regression selects root/nested indexes, log, and an ordinary page but admits only the ordinary page to factual reads/provider input. Catalog gates cover independent ceilings, preserved original Markdown, nested reachability, escaping, duplicate/no-op additions, forbidden raw catalog edits, combined plan bounds, and stale preconditions.

Fixed request measurements are regression evidence rather than production capacity or token estimates:

| Fixture | Measurement and required result |
| --- | --- |
| Four-page UTF-8 fixture | Content-only wire 2,230,845 bytes; complete v5 SDK prompt 2,239,474 bytes; both fit 4,194,304 bytes, with exact measured/actual equality and typed envelope/provenance comparison |
| Provider capped at single-page envelope | 558,270 bytes must reject before runtime creation/inference because the full wrapper costs more |
| Eight large pages followed by a small page | Retains seven large pages, omits the eighth, includes the complete small page; actual SDK request 3,915,441 bytes; report has eight included and one omitted candidate |

Complete source/schema/catalog input and included pages remain intact. Exact and one-byte-over boundaries cover application requests and actual SDK prompts, including a mismatched sizing wrapper, JSON escaping, UTF-8, schema base64, long identities, cancellation, unsafe extension marshalers, and impossible required inputs. Failures preserve raw evidence and create no stage/canonical changes. Controls/duplicate IDs do not consume factual allowance; omissions consume the unique candidate count.

Real `FilePlan` tests reject edits to omitted existing pages even with correct digests and preserve unrelated prose, citations, and stale human-write protection on complete updates. Typed cap/format changes distinguish generations before reservation. Frozen historical payloads include v1 `9e9edf0`, v2 `fddb1a3`, v3 `0a1d133`, and v4 `c929d69`; genuine v1–v5 fixtures cover queued rejection and authenticated concrete-stage/terminal replay without embedding or maintainer inference under contract v6.

### Public Ingest and Replay

Actual Host HTTP/MCP fixtures cover URI references and supplied content with URL origins. They assert raw/provider content equality, default media types, source identity, and zero URL fetches. Terminal idempotent replay returns the same completed operation without new inference or fetching. Missing/conflicting payloads fail through HTTP machine-readable `invalid_request` and MCP typed `ErrInvalidArguments`. Golden knowledge-loop, restart, projection, cancellation, shutdown, lease-loss, and filesystem recovery suites retain their existing controls.

## Generic Lexical Retrieval Evaluation

Shared `searchtest`/`contexttest` corpora exercise actual SQLite and integration-tagged PostgreSQL. Independently authored originals and expected IDs cover canonical accents, English/Russian casing, mixed technical words, accent/whole-word negatives, and literal question words. A two-rune excerpt must preserve original `e` plus combining accent.

Strict controls cover original titles, provenance, OKF fields, strict-then-relaxed ordering, semantic field priority, path ties, scope/source filters, cancellation, and deterministic rebuild. Tests do not require numeric native-score equality or arbitrary identical ordering across backends.

Bounds tests cover raw query/source limits, terms/runes, 256-entry/16-unique source filters, snippet defaults/clamping, and projection ceilings. PostgreSQL really indexes/searches a maximum 1,640-byte private term, 8,192 distinct words, and an exact 524,288-byte page stream; typed SQL measures native vector size. One-byte overflow and failed/canceled rebuilds preserve prior projection. Migration 14→15, reopen/rebuild, and Down invalidate incompatible tokens/readiness while preserving original and durable operation/source/document state. A real filesystem/SQLite `QueryService` fixture preserves original evidence/citations without writes or inference.

Two fresh baseline runs yield identical nine decoded observations. `generic-cyrillic-case` and `canonical-accent` are strict `met` controls. Inflected Russian forms, the English plural, and semantic paraphrase remain lexical `gap` observations that may improve. These results describe a fixed corpus.

## Real CPU Embedding Quality

The integration-tagged `pkg/knowl/store/eval` runner calls actual pinned TEI/E5 APIs and real SQLite/PostgreSQL. Its corpus was reviewed before model results: nine baseline cases, two independently authored English/Russian positives with zero overlap under the real lexical normalizer, exact-title/mixed-token controls, and 22 technical distractors including negation. Expected IDs and k stay fixed.

Every positive must appear in Query top five and the first five direct source relevance seeds from `SelectContext` with eight pages, before neighbor/root/recent fallback. Unique exact titles rank first, and relevant evidence must outrank designated distractors. Both stores repeat twice and compare ordered IDs, original evidence/citations, filters, updates, deletion, foreign scope, and cancellation. The separate unchanged golden corpus requires at least 11/12.

| Recorded profile | Query/source top-five | Golden | Negated Russian rollout control |
| --- | --- | --- | --- |
| E5-small, 384 dimensions | 13/13 | 12/12 | Failed: rollback rank 2, distractor rank 1 |
| E5-base, 768 dimensions | 13/13 | 12/12 | Passed: rollback rank 1, distractor rank 2 |

E5-base passes all gates on both stores and repeats. Records are `pkg/knowl/store/eval/testdata/cpu-e5-base.json` and retained failed `cpu-e5-small.json`. They include image/model revision, immutable corpus hash, per-case lexical/hybrid/source ranks and cosines, timing, allocation, machine, and memory. The model changed with explicit approval while corpus, k, expected IDs, and algorithm stayed fixed. Results do not establish universal recall, contradiction detection, or semantic duplicate prevention.

The reference uses `intfloat/multilingual-e5-base` revision `d128750597153bb5987e10b1c3493a34e5a4502a`, mean float32 normalized 768-dimensional output, and CPU TEI 1.9.0. The recorded image is:

```text
ghcr.io/huggingface/text-embeddings-inference:cpu-1.9.0@sha256:bc7ad262695df5b7875b0c9c702deb8e9df3953bdf22c3d6068d9c0429b7b3f3
```

TEI received two CPUs/4 GiB; the daemon was x86_64 with four CPUs and 33,657,823,232 bytes RAM. Cold readiness measured 194.5 seconds, cached restart 6.6 seconds, cache 1,075 MiB, and idle memory about 1.91 GiB. Network-disabled cached startup, 768-vector inference, token overflow/alias errors, restart equality, and actual fallback/strict outage behavior were verified. These observations are not deployment guarantees.

### Complete-Page Coverage Measurements

An authorized private 51-page wiki snapshot had page-digest-set SHA-256 `ad2518dd403ce09f810085b78a1e3a745ba60e0c8989a8da0d9d1f8bf8f5654c`. Comparison held E5-base revision, prefixes, CPU TEI server, and queries constant.

| Measurement | First 16 chunks/page | Complete pages |
| --- | ---: | ---: |
| Indexed chunks | 644 | 1,272 |
| Omitted normalized runes | 192,332 across 31 pages | 0 |
| Rebuild seconds | 89 | 177 |
| Exact-title top-1 | 51/51 | 51/51 |
| Existing 25 paraphrases, top-5 | 19/25 | 19/25 |
| Three head / middle / tail questions, top-5 | 2/3 · 2/3 · 3/3 | 2/3 · 3/3 · 3/3 |
| Three tail questions, top-1 | 1/3 | 2/3 |
| Paraphrase search p50 / p95, milliseconds | 95 / 106 | 132 / 156 |

Complete indexing reached at most 79 chunks for one page; both projections fit 8,192 chunks. Direct source-selection top-five counts matched search, and two passes yielded the same ranks. The nine section questions were authored/labeled before comparison; together with the older 25 they form a small diagnostic set rather than traffic-weighted judgments.

Tail pages already appeared in top five before the change. A separate model probe found the best window beyond ordinal 15 for two tail questions and within the first 16 for the third. All three result excerpts lacked their selected tail marker because lexical snippets took priority on mixed-channel hits. A deterministic dense-only regression verifies winning tail-window original text. Complete coverage makes text available to the model but guarantees neither a tail winner nor top-k placement. Timings include API runtime/exact scan; private text was not committed.

### Running Model Evaluation

For an already reachable reference service and disposable PostgreSQL fixture:

```bash
export KNOWL_EMBEDDING_EVAL_ENDPOINT='http://your-tei:80/v1/embeddings'
export KNOWL_EMBEDDING_EVAL_POSTGRES_DSN='postgres://fixture-user:fixture-password@your-postgres/fixture-db?sslmode=disable'
export KNOWL_EMBEDDING_EVAL_OUTPUT='/tmp/knowl-embedding-quality.json'
go test -tags integration -count=1 -v ./pkg/knowl/store/eval \
  -run 'TestQualityCorpus|TestRealCPUModel'
```

The gate checks TEI `/info` identity/settings in addition to vectors, separately from Knowl's generic API protocol. Absent endpoint explicitly skips local model tests; a configured endpoint requires PostgreSQL. Skipping never counts as a passing model gate.

The Compose profile exposes no TEI host port. TEI-only evaluation uses project `knowl-evaluation` and private network `knowl-evaluation_default`; Compose still resolves Knowl-only variables, so unused values are supplied:

```bash
OPENAI_API_KEY=unused OPENAI_MODEL=unused KNOWL_OPERATOR_TOKEN=unused \
  docker compose -p knowl-evaluation -f deploy/sidecar/compose.yaml \
  -f deploy/sidecar/embeddings.compose.yaml up -d tei
```

Wait for health, create a disposable PostgreSQL fixture on that network, and compile the evaluation binary with:

```bash
CGO_ENABLED=0 go test -c -tags integration \
  -o /tmp/knowl-embedding-eval.test ./pkg/knowl/store/eval
```

The documented container invocation mounts the binary read-only, mounts `.artifacts/embeddings` for results, uses the caller's UID/GID, and runs `-test.run 'TestQualityCorpus|TestRealCPUModel' -test.v -test.timeout 10m` on the private network with the three evaluation variables. Output contains bounded safe numeric/identity results; comparable records also need actual image, machine, allocation, memory/cache, and cold/warm observations. Cleanup removes the disposable database and runs Compose `down` while retaining the model cache.

## Regression and Delivery Gates

Ordinary pre-push checks are:

```sh
go test ./...
go tool golangci-lint run ./...
go run github.com/fe3dback/go-arch-lint@v1.15.0 check --project-path .
```

Container-backed PostgreSQL coverage remains behind `integration` and needs a working container runtime:

```sh
go test -tags integration -count=1 ./pkg/knowl/store/postgres \
  -run TestStoreContractWithTestcontainers
```

Focused race checks include:

```sh
go test -race -count=1 ./pkg/knowl/store/sqlite \
  -run 'TestSearchContract|TestContextContract|TestContextBaseline|TestSQLiteGeneric'
go test -race -tags integration -count=1 ./pkg/knowl/store/postgres \
  -run TestStoreContractWithTestcontainers
```

Full native/race/lint/architecture checks and actual tagged PostgreSQL contracts remain delivery gates. Output-policy and operation-report changes also repeat OpenAPI generation to confirm no drift. Focused races aid diagnosis but do not replace the full race run. Story closure requires required checks on the exact PR head and all Story changes merged.

### Gradual Source Retirement

Retirement tests follow actual consistency boundaries rather than asserting a global cutover:

- Shared lifecycle contracts keep equal-ID filesystem/Git owners independent and exercise active/retiring/deleted CAS, restart, cancellation, re-addition, and concurrent finalization.
- Runtime/coordinator tests wait for all additions to become ready before discovery; pure deletion proceeds immediately.
- Real staging rejects retained/new retiring refs with typed owner/path diagnostics while pre-transition canonical pages remain readable.
- Planner/workspace tests delete old-only pages with exact catalog closure/preimages and recompute mixed pages from sorted active evidence while preserving memberships.
- Typed failures cover missing, unknown, undeclared, stale-page/catalog, lifecycle-generation, and evidence-limit plans.
- Recovery injects faults before the journal, after preimages, after every target, after durable commit, and after receipt creation.
- Durable page fixtures keep A failed while B commits, then retry only A; planning/staging/commit/projection/outcome/cancellation/stale failures preserve pages and bounded diagnostics.
- Multi-window verification/store CAS finalizes only a fresh authoritative zero-reference round without remaining work.
- Safe-subset tests retain `plan.empty_safe_subset` and accumulated page/catalog path/target diagnostics; ordinary lineage removal remains invalid.
- Typed status, CLI JSON, HTTP routing, targeted dry-run/retry, pager bounds, owner rotation, fairness, cursor mutation, receipts, lease expiry, and projection replay cover recovery without log-text assertions.

Focused commands are:

```sh
go test -count=1 ./pkg/knowl/app ./pkg/knowl/content/fs \
  ./pkg/knowl/store/sqlite ./internal/source/reconcile \
  ./internal/httpapi/operatorapi ./internal/httpapi/server ./cmd/knowl
go test -tags integration -count=1 ./pkg/knowl/store/postgres \
  -run 'TestPostgresSourceLifecycleContract|TestStoreContractWithTestcontainers'
```

### Durable Operation Diagnostics

Application fixtures capture actual maintainer inputs and serialized request sizes, whole-page omissions, exact/unknown required overflow, partial reads, cancellation, provider failure, and refused report writes. Cancellation after fitting retains `assembled` evidence with zero inference. Twenty long valid IDs force reporting truncation without changing model pages or complete fitting counts. Strict projection errors retain the original check without invented assembly.

Shared stores test immutable attempt snapshots, current/older attempts, scope/terminal guards, idempotent identical writes, conflicting concurrent writes, reopen/claims, malformed/oversized values, and migration 17 Up/Down. Known zero staged file counts differ from unknown legacy counts; opaque historical digests remain private. Allocation gates catch oversized report/digest loading. Missing/null required report counters or attempt fields return the invalid-report sentinel rather than invented zeros, including real HTTP/MCP reads.

`TestOperationDetailsHTTPAndMCPDurableParity` ingests through real filesystem/SQLite, reopens storage, and compares generated HTTP and MCP results for queued, committed, provider-failed, and required-overflow operations. The committed case uses application-invalid then valid output, retaining correction after reopen and distinguishing two physical turns from one scheduler attempt. Custom fallback omits physical measurements. Polling/replay neither invokes inference nor changes pages. Parsed public JSON is checked for private source/query/rationale/upstream-error sentinels.

Bounded historical port tests cover escaped IDs, maximum safe counters, truncation, older producing attempts, absent legacy facts, and opaque digests. Actual serialization must stay below 96 KiB; indexes reject search/selection during operation reads. These interface/storage tests require no source-substring assertions or new embedding experiment.

### Bounded Output Correction

`TestRuntimeCorrectionSequence` uses the pinned structured runner with controlled output and real application/filesystem/SQLite boundaries. It emits case ID, measured report, and `met` only after strict assertions pass.

```sh
go test -count=1 -json ./pkg/knowl/... ./internal/httpapi/server \
  -run 'TestRuntimeCorrection|TestIngestCorrection|TestHierarchyCorrection|TestSourceCorrectionPolicy|TestIngestReservesFeedback|TestHostOutputConfiguration|TestPublicOperationCorrection|TestOperationDetails' \
  > /tmp/knowl-output-correction.jsonl
```

Gates cover valid-first output; malformed/schema/branch/application rejection followed by valid output; zero allowance, exhaustion, and mixed layers sharing one correction allowance. Intermediate invalid plans cannot stage/save. Exact/overflow collector accounting covers partial/final text and thought/error events; feedback contains only allowlisted codes over unchanged requests. Runtime wait/build/session/across-turn deadlines and cancellation retain attribution. Transport failures remain retryable with one generation; exhaustion is permanent. Exact-fit feedback reservation, historical generation fencing, authenticated recovery, store corruption guards, and HTTP/MCP maximum details remain enforced. Fixtures set readiness and expired leases through real SQLite APIs rather than timing tricks. No runtime upgrade or real embedding experiment is required for these policy changes.

### Bounded Execution Ownership

Run the fixed evaluation twice:

```sh
go test -count=2 -json ./pkg/knowl ./pkg/knowl/app \
  -run 'TestContextBaselineBlockedExecution|TestHostComposesDistinctRuntimeSessions|TestConcurrentPreparedSourcePlansFailSafely|TestOverlappingSourceAndHierarchyPreserveFirstCommit' \
  > /tmp/knowl-concurrency-eval.jsonl
```

Capacity one requires `first_started`, `first_released`, `second_started`; capacity two requires `first_started`, `second_started`, `first_released`. Default one records `gap` for parallel progress, while configured two requires `met`. Controlled barriers make repeated observations agree without using elapsed time as a benchmark.

`isolated-runtime-owners` measures owner/session bindings, close counts, and source calls. `source-source-conflict` and `source-hierarchy-conflict` measure actual model calls and safe outcomes: first-committed facts/log survive, with exactly two source calls or one source plus one hierarchy call and no automatic replanning.

Real boundaries cover rejected configuration before startup, two active owners with a third unclaimed, exact WorkAttempt counts, session isolation/reuse, shared scheduler/Drain/hierarchy/lint capacity, shutdown retaining live owners until later Stop, independent renewal loss, projection ordering, canceled source-sync publication/recovery, and failed setup/cleanup ownership. Controlled concurrency establishes integrity and execution behavior, not production throughput or two successful commits from overlapping plans.

## Native Vector Comparison Method

`BenchmarkVectorRuntime` calls real `SearchWithReport`, including scalar readiness/snapshot/model admission, source filtering, fusion, and consumed-evidence validation. Publication/reuse/recovery and invalidation establish integrity; reads do not repeat population-wide audits. Actual-driver/structured-plan tests cover search/context/health admission; startup/lint composition tests retain repair/failure behavior.

`BenchmarkVectorKernel` uses separate disposable tables to compare native scalar/page SQL with isolated full-vector-transfer/Go-dot scoring. Both kernel paths omit production validation, caps, inference, fusion, and evidence. Their results are not supported-runtime throughput. Native cosine and historical near-unit dot may legitimately choose different winners.

Default fixtures contain 128 chunks, eight dimensions, and one/four query windows. For an actual runtime before/after comparison, use a detached temporary worktree at `dec6249b68a27435217e29bef9a5669fee068cb0`, copy identical `internal/searchtest/{vector_benchmark,native_oracle}.go` and backend `vector_runtime_benchmark*_test.go` files, and leave production code unchanged. Label `KNOWL_VECTOR_BENCH_REVISION` with each actual revision.

```sh
go test -v -json ./pkg/knowl/store/sqlite -run '^$' \
  -bench '^BenchmarkVectorRuntime' -benchtime=5x -benchmem -count=1
go test -v -json -tags integration ./pkg/knowl/store/postgres -run '^$' \
  -bench '^BenchmarkVectorRuntime' -benchtime=5x -benchmem -count=1
go test -v -json ./pkg/knowl/store/sqlite -run '^$' \
  -bench '^BenchmarkVectorKernel' -benchtime=5x -benchmem -count=1
```

PostgreSQL uses operator-provisioned pgvector 0.8.2/PostgreSQL 16 in a pinned disposable container; its kernel also needs `integration`. No live workspace or external model service participates. Seed 73 and prepared-input hashes produce normalized float32 vectors outside timing. Runtime timing retains local provider lookup/copy with zero external inference. Checksums bind prepared inputs/vector bits, source membership, dimensions, and windows. Native correctness uses an exhaustive scalar/page oracle and a separate original-float32 float64 cosine check with the unchanged dimension-dependent bound.

Opt-in `KNOWL_VECTOR_BENCH_LAB=1` enumerates 8,192/25,000/100,000 chunks, dimensions 2/768/4,096, one/four windows, and 50% source eligibility, plus 8,192×768 at 100%. Production capacity remains unchanged: oversized publication returns typed `projection_capacity` rather than supported timing. Kernel allocation estimates six float32 representations plus 512 bytes/chunk and reports `resource_limited` above 512 MiB. A 180-second per-case context starts before fixture preparation, migrations, and container startup and covers setup, correctness/plans, and repetitions. Unsupported cases carry no timing claim.

Observations include wall samples, sample count/median/min/max, ns/op, B/op, allocs/op, typed retrieval reports, and actual transfer counts. Compatibility `P95NS` at n=3 is nearest-rank maximum, not reliable p95 evidence. New runtime records include one publication and authenticated reuse setup cost outside warm-query timing; single values are not distributions. `ScannedChunks` is admitted published population. Bytes mean decoded logical result cells, excluding wire framing/storage; `LogicalScoredPairs` is an estimate rather than executor work.

SQL intervals include client row consumption and are neither server CPU nor independent additive median phases. Refined scalar counters include guard/fixed metadata rows; historical counters retain original definitions. PostgreSQL parses numerical `EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON)` work; SQLite records structured `EXPLAIN QUERY PLAN` without invented executor counters. Bound vectors and raw SQL are not serialized. Comparative reports need hardware, versions/settings, revisions, and exact commands; smoke samples and isolated kernels imply no production speed promise.

### Trusted Publication Comparison — 2026-10-17

Current supported-query results are mixed. SQLite improves in most measured fixtures, but its two-dimensional/four-window case remains slower than the refreshed Go baseline. PostgreSQL remains slower with 50% source eligibility and improves in the measured all-source case. These observations establish no general latency improvement or SLO.

Current native production revision is `643933d61e82ff5d396ae5f3db3a13d57ba1c840`; the actual legacy production baseline is `dec6249b68a27435217e29bef9a5669fee068cb0`. Pairs ran strictly sequentially on the same host, with three warm queries per case. Four runtime/fixture/oracle harness files were byte-identical across checkouts. All fixtures retained seed 73, original prepared vectors, 128 pages, page limit 5, dense channel limit 20, and historical fixture checksums.

All rows below use 8,192 chunks. Values are milliseconds, median followed by observed minimum–maximum. The historical column belongs to a separate earlier run; it cannot support a causal speedup factor. OS/cache/background effects remain uncontrolled, and three samples do not establish reliable tails.

| Backend | Dimensions/windows/source % | Refreshed Go | Trusted native | Historical v1.3 native median |
| --- | --- | ---: | ---: | ---: |
| SQLite | 2/1/50 | 67.593 (66.382–69.721) | 38.830 (38.455–39.545) | 1174.389 |
| SQLite | 2/4/50 | 69.634 (69.594–70.957) | 73.098 (69.270–73.840) | 1183.050 |
| SQLite | 768/1/50 | 426.163 (425.573–436.138) | 68.132 (64.332–69.368) | 2929.833 |
| SQLite | 768/4/50 | 452.554 (438.680–465.684) | 138.758 (137.375–156.208) | 3102.299 |
| SQLite | 768/1/100 | 462.949 (461.335–467.712) | 103.451 (101.413–112.053) | 3077.691 |
| PostgreSQL | 2/1/50 | 62.210 (60.539–62.291) | 168.249 (167.172–171.770) | 198.676 |
| PostgreSQL | 2/4/50 | 66.439 (63.545–67.300) | 176.600 (173.507–177.024) | 210.722 |
| PostgreSQL | 768/1/50 | 157.079 (155.512–158.211) | 186.948 (178.318–188.651) | 807.677 |
| PostgreSQL | 768/4/50 | 187.183 (184.219–194.752) | 214.689 (213.777–217.588) | 825.127 |
| PostgreSQL | 768/1/100 | 146.657 (142.780–147.521) | 59.517 (57.207–61.740) | 739.054 |

The source artifact `docs/testing/native-vectors.json` retains historical top-level observations/plans and appends harness hashes, exact commands, hardware/settings, raw-log checksums, 20 current observations, and 20 actual structured plans under `TrustRefinement`.

The approved trust refinement retains complete projection validation during publication, reuse, and recovery, atomic ready publication, and invalidation on incompatible page changes. Ordinary caller paths admit bounded scalar readiness/snapshot/model state and validate original evidence when consumed. Arbitrary nonselected SQL corruption is outside the application-owned per-read trust boundary. The refinement adds no schema, cache, generation, setting, provider, scoring rewrite, or capacity increase.

Every native query in these search fixtures transferred two scalar admission rows: 276 logical result-cell bytes for SQLite and 259 for PostgreSQL. Coverage-manifest and stored-vector transfer were zero; queries returned 20 dense candidates, five evidence rows, and 32 total driver rows including page fetches. `ScannedChunks=8192` describes the admitted full population. Exhaustive eligible native cosine still runs. Bytecode/catalog-root and typed PostgreSQL-plan tests cover larger populations and search/context/health, while actual startup/lint composition tests retain repair/failure checks.

At 768 dimensions, one window, and 50% source eligibility, Go allocation volume fell from 79.904 to 1.684 MiB/query on SQLite and 79.974 to 1.764 MiB/query on PostgreSQL. These are B/op measurements, not RSS. The old path transferred 8,192 stored vectors, or 24 MiB/query; native still transfers current pages and evidence bodies.

For that fixture, mean scalar SQL row-consumption intervals were 0.132 ms/1.101 ms and ranking intervals 61.867 ms/173.686 ms for SQLite/PostgreSQL. They include client consumption and cannot be interpreted as pure server CPU or added as independent median phases. Refined scalar counts include the fixed metadata row; historical counts retain their definition, and legacy scalar classification includes coverage rows. The source-filter contrast alone does not identify a causal hotspot.

One full `Rebuild` (`PublicationNS`) and one same-snapshot `Project` (`ReuseNS`) were measured outside query timers per case. They include lexical work, complete validation, and writes. For the same 768-dimensional fixture, native Rebuild/Project took 1.439/2.305 seconds on SQLite and 6.926/8.507 seconds on PostgreSQL. Each is a single setup observation rather than a latency distribution or isolated validator benchmark.

All ten current native cases passed exhaustive native/page aggregation and independent float64 cosine checks. Maximum observed errors were 2.10326643e-7 on SQLite and 9.97581830e-8 on PostgreSQL, within the unchanged dimension-dependent bound. A separate frozen-harness SQLite 8,192×2/one-window/50% reproduction measured median 38.310 ms, range 36.936–38.727 ms, n=3; it is retained separately under `TrustRefinement.Reproduction` rather than pooled.

Larger capacity rejections, isolated kernels, and high-dimensional resource outcomes remain measurements from historical revision `6d4ee134fad6d19f6d4efbd3150d0b174f0e7cb4`; they were not rerun for this comparison. Review found kernel/scoring/fixture/oracle and production limits unchanged. TASK-018 reran supported bounds, oracles, publication/reuse, CAS/invalidation, failure/cancellation, CGO-free, PostgreSQL, and race coverage.

### Historical v1.3 Supported-Query Regression

At native revision `6d4ee134fad6d19f6d4efbd3150d0b174f0e7cb4`, supported full-query latency materially regressed on both backends despite eliminating stored-vector transfer. The actual Go baseline used `dec6249b68a27435217e29bef9a5669fee068cb0`, with byte-identical shared harness files and matching fixture checksums. Its real guards, full-scope transfer, Go validation/ranking, fusion, and evidence remained active.

Each row uses 8,192 chunks, 128 pages, page limit 5, dense limit 20, and three warm queries. Setup, publication, correctness, and plan collection preceded timing; actual `SearchWithReport` timing included all then-current query integrity, filtering, fusion, provider lookup/copy, and evidence.

| Backend | Dimensions/windows/source % | Legacy median ms | Native median ms | Native/legacy |
| --- | --- | ---: | ---: | ---: |
| SQLite | 2/1/50 | 58.649 | 1174.389 | 20.02× |
| SQLite | 2/4/50 | 59.940 | 1183.050 | 19.74× |
| SQLite | 768/1/50 | 365.492 | 2929.833 | 8.02× |
| SQLite | 768/4/50 | 401.393 | 3102.299 | 7.73× |
| SQLite | 768/1/100 | 394.649 | 3077.691 | 7.80× |
| PostgreSQL | 2/1/50 | 53.756 | 198.676 | 3.70× |
| PostgreSQL | 2/4/50 | 56.752 | 210.722 | 3.71× |
| PostgreSQL | 768/1/50 | 140.520 | 807.677 | 5.75× |
| PostgreSQL | 768/4/50 | 168.442 | 825.127 | 4.90× |
| PostgreSQL | 768/1/100 | 121.107 | 739.054 | 6.10× |

A separate earlier SQLite 8,192×2/one-window reproduction measured median 1.115 seconds, range 1.111–1.163 seconds, and remains separate from the matrix. These regressions describe performance rather than data-integrity failure; their measurements did not authorize weaker integrity checks. The later approved trust refinement is reported separately above.

At 8,192×768/one-window/50%, historical SQLite allocations changed from 79.90 MiB/177,457 allocations to 1.74 MiB/3,201; PostgreSQL changed from 79.97 MiB/177,437 to 1.83 MiB/3,203. Old transfer was 8,192 vectors/25,165,824 bytes versus zero native stored vectors. Native returned 20 candidate rows, five evidence rows, and 39 total driver rows; full-scope `ScannedChunks` remained 8,192. Legacy also reported 20 logical candidates but zero SQL-scored candidate rows because ranking ran in Go.

Historical mean scalar row-consumption intervals were 2,876.0 ms/706.7 ms and ranking intervals 51.3 ms/95.4 ms for SQLite/PostgreSQL. They support scalar/validation-work dominance within that revision but are not pure CPU measurements or additive median phases. The PostgreSQL 8,192×768/one-window plan observed 768 vector-element rows across 8,192 loops, or 6,291,456 element visits.

A separate PostgreSQL 100,000×2/four-window kernel EXPLAIN observed 200,000 scored rows, a 20-row root, 5,275 shared-hit blocks, zero shared reads, 516 temporary-read/3,834 temporary-written blocks at the root, and 274.739 ms root executor time. Cumulative parent/child buffers must not be summed, and instrumented EXPLAIN time is separate from query wall samples.

### Historical Isolated Kernels and Capacity Outcomes

Both isolated paths omit production readiness/integrity, capacity, inference, lexical search, fusion, and original evidence. They use identical original vectors/source memberships in separate disposable tables, 128 pages, eight stronger foreign-scope exclusions, and 20 winners. Numerical correctness runs outside timing; native exact ordered winners are also checked in every measured iteration. All 12 feasible fixtures per backend ran both modes with three samples.

| Backend | Chunks×dimensions | Windows | Legacy median ms | Native median ms |
| --- | --- | ---: | ---: | ---: |
| SQLite | 25,000×768 | 1 | 276.206 | 167.892 |
| SQLite | 25,000×768 | 4 | 236.812 | 356.603 |
| SQLite | 100,000×2 | 4 | 424.798 | 761.148 |
| SQLite | 1,024×4,096 | 1 | 57.585 | 61.288 |
| PostgreSQL | 25,000×768 | 1 | 220.393 | 80.388 |
| PostgreSQL | 25,000×768 | 4 | 247.370 | 175.019 |
| PostgreSQL | 100,000×2 | 4 | 119.449 | 233.036 |
| PostgreSQL | 1,024×4,096 | 1 | 45.388 | 6.200 |

At 100,000×2, legacy kernel transfer was 100,000 vectors/800,000 bytes per query; native returned 20 metadata winners and zero stored-vector bytes. At 25,000×768, legacy isolated transfer was 76,800,000 bytes/query. These mixed kernel timings neither establish supported production capacity nor explain away full-query regressions.

Both actual backend publication APIs rejected 25,000×2, 100,000×2, and 8,192×4,096 with typed `projection_capacity` under unchanged 64 MiB logical limits. Those six observations have zero samples and no supported query timing. The 8,192×768 fixtures fit both byte and coverage limits.

For both backends, isolated resource screening rejected 8,192×4,096, 25,000×4,096, 100,000×768, and 100,000×4,096 with either one or four windows before vector/database allocation. The 100,000×4,096 estimate was 9,881,600,000 bytes against 536,870,912 allowed. A 1,024×4,096 fixture provided a bounded actual high-dimensional run. These outcomes are explicit resource/capacity results rather than timing extrapolations.

### Numerical Correctness and Laboratory Identity

Historical native fixtures passed exhaustive scalar enumeration with independent Go source/page/ordinal aggregation and a separate true float64 cosine check over original float32 values. The numerical bound is `epsilon(d)=8*gamma(d+2)+8*u`, with `u=2^-24` and `gamma(n)=n*u/(1-n*u)`. It is never a production ordering epsilon. Historical maximum errors were 2.55914505e-7 on SQLite and 1.17177782e-7 on PostgreSQL, passing each fixture's dimension-dependent bound. Independent oracles also cover dimensions 1/2,000/2,001/4,096, near-unit A/B reversal, crowding, later windows, exact/close ties, and reverse insertion.

Legitimate historical 8,192×2/four-window/50% ordered winners were:

- Legacy, both stores: `p000086,p000072,p000114,p000006,p000090`.
- SQLite native: `p000086,p000072,p000006,p000114,p000090`.
- PostgreSQL native: `p000072,p000086,p000006,p000114,p000090`.

Each native exact oracle passed. Native true cosine, backend float32 arithmetic, and old clamped near-unit float64 dot need not preserve near-match order or legacy parity. Random fixed vectors test arithmetic and selection rather than semantic relevance.

Recorded hardware/software were Go 1.26.9, Linux `6.18.26.1-microsoft-standard-WSL2`, amd64, four vCPUs on AMD Ryzen 7 PRO 7840U, 33,657,823,232 bytes RAM, no swap, SQLite 3.53.3/sqlite-vec 0.1.9, and PostgreSQL 16.14/pgvector 0.8.2. Historical available RAM was about 24.6 GB and temporary-filesystem space 583,416,229,888 bytes. Current comparisons used the same host. This is a single-host laboratory with uncontrolled background/cache effects.

PostgreSQL used one pooled connection, local TCP, `shared_buffers=128MB`, `work_mem=4MB`, `jit=on`, and pinned image:

```text
pgvector/pgvector:0.8.2-pg16@sha256:00ba258a66dac104fd5171074a0084462a64a1369d8513f3d0a634e2f24d15bc
```

SQLite used one pooled connection, WAL, `foreign_keys=1`, and `busy_timeout=5000ms`. Deterministic PCG/input-SHA256 normalized float32 vectors used seed 73 and were prepared outside query timing. External inference calls were zero, while local provider lookup/copy remained timed. Fixtures accessed only temporary SQLite files/disposable PostgreSQL, with no live wiki, UI, or external provider.

Each case's 180-second context covered setup, correctness/plans, and repetitions. The 512 MiB kernel estimate is not an OS-enforced RSS quota. Historical raw Go JSON command logs/exits were retained under `/tmp/knowl-qqz15-*`; committed JSON preserves parsed observations/plans without raw SQL or query vectors. TASK-014 failed/smoke attempts remain in a separate ledger and are excluded from matrix samples. No real-provider end-to-end latency or model-quality claim follows from these measurements.
