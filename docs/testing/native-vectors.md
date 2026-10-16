# Native vector retrieval: local comparative results

## Outcome: material supported-query latency regression

The native implementation transfers no stored vectors into Go, but **full query
latency regressed on both backends** in this bounded laboratory. At 8192 chunks,
768 dimensions, one window and 50% source eligibility, the measured native median
was 2.930s versus 0.365s previously on SQLite (8.02×), and 0.808s versus 0.141s on
PostgreSQL (5.75×). This is a material performance regression at a supported
size, not a data-integrity failure. It must remain visible before any publishing
or merge decision. Do not advertise a latency improvement. Optimizing validation
is separate, unimplemented work; these measurements do not authorize weakening
integrity checks.

The complete typed observations, actual wall samples, allocation metrics,
transfer counts, ordered result IDs and parsed DB plans are in
[native-vectors.json](native-vectors.json). No production bound was raised.

## Paired full-query measurements

All rows use 8192 chunks distributed over 128 pages, page limit 5 and dense
candidate limit 20. Each entry has **n=3** measured warm queries. Tables report
medians; raw JSON contains each sample and observed min/max. Its `P95NS` is merely
nearest-rank=max of 3, **not a reliable population tail estimate or confidence
claim**. Setup, publication, correctness and plan collection precede timing;
the timed real `SearchWithReport` call includes all query-time integrity, source
filtering, fusion, local fixed-vector provider lookup/copy and current evidence.

| Backend | dims | windows | source % | Legacy median ms | Native median ms | Native/legacy |
|---|---:|---:|---:|---:|---:|---:|
| sqlite | 2 | 1 | 50 | 58.649 | 1174.389 | 20.02× |
| sqlite | 2 | 4 | 50 | 59.940 | 1183.050 | 19.74× |
| sqlite | 768 | 1 | 50 | 365.492 | 2929.833 | 8.02× |
| sqlite | 768 | 4 | 50 | 401.393 | 3102.299 | 7.73× |
| sqlite | 768 | 1 | 100 | 394.649 | 3077.691 | 7.80× |
| postgres | 2 | 1 | 50 | 53.756 | 198.676 | 3.70× |
| postgres | 2 | 4 | 50 | 56.752 | 210.722 | 3.71× |
| postgres | 768 | 1 | 50 | 140.520 | 807.677 | 5.75× |
| postgres | 768 | 4 | 50 | 168.442 | 825.127 | 4.90× |
| postgres | 768 | 1 | 100 | 121.107 | 739.054 | 6.10× |

Before is actual production code at
`dec6249b68a27435217e29bef9a5669fee068cb0`; after is
`6d4ee134fad6d19f6d4efbd3150d0b174f0e7cb4`. The four shared runtime/oracle files
were byte-identical in both checkouts. The old pipeline retains its real guards,
full scope transfer, Go validation/ranking and fusion/evidence. It is not native
validation with a fabricated old ranking stage. Each paired fixture checksum
matches. A separate SQLite 8192×2/one-window reproduction yielded median 1.115s,
range 1.111–1.163s, before the matrix's 1.174s median; exact samples/checksum are
retained separately in JSON.

For 8192×768/one-window/source 50, Go allocations fell from 79.90 MiB/177457
allocations to 1.74 MiB/3201 on SQLite, and 79.97 MiB/177437 to 1.83 MiB/3203 on PG.
These are per-query averages, not peak RSS. Old queries transferred 8192 stored
vectors/25165824 payload bytes (24MiB), while native transferred 0. Native queries
returned 20 candidate and 5 evidence rows, 39 total driver rows; full-scope
`ScannedChunks` remained 8192, not 20. The old report also had 20 logical dense
candidates, but zero SQL-scored candidate rows because Go ranked them. Metadata
coverage and current page/evidence bodies still transfer: zero vectors is not
zero result bytes or O(k) integrity work.

For the same fixture, average native scalar-result SQL row-consumption intervals
were 2876.0ms (SQLite)/706.7ms (PG); ranking intervals 51.3ms/95.4ms. They support
validation/scalar-work dominance, but include client consumption and are not pure
DB CPU, a complete Go-integrity split, or independent additive median phases.
PG structured plans carry actual executor rows/loops/times/buffer work. SQLite
plans describe strategies only; logical scored-pair counts are estimates, not
invented executor counters. All detailed plan nodes are preserved in JSON.
For example, the native PG 8192×768/one-window plan observed a vector-element
function scan with 768 rows×8192 loops (6291456 element visits). Its 100000×2/
four-window isolated plan observed 200000 scored rows, a 20-row root, 5275 shared
hit blocks, zero shared reads, 516 temporary read/3834 temporary written blocks
at the root and 274.739ms root executor time during the separate EXPLAIN sample.
Do not sum cumulative buffer counts across parent/child nodes or substitute this
instrumented plan timing for the n=3 query wall distribution.

## Isolated kernels and unsupported capacity

Isolated results are recorded separately in JSON. Both native SQL/page maxima
and legacy full-vector-transfer/Go-dot kernels omit production readiness,
full-scope integrity, capacity constraints, inference, lexical search, fusion
and original evidence. They use separate disposable tables, identical original
vectors/source membership, 128 pages, eight stronger foreign-scope exclusions and
20 page winners. They cannot establish production throughput or explain the
full-query regression away. Numerical correctness is checked outside timing;
native exact ordered winners are also checked during every measured iteration.

All 12 feasible kernel fixtures per backend ran both modes with n=3. Results
were mixed; selected examples below are isolated, not production capacity:

| Backend | chunks×dims | windows | Legacy median ms | Native median ms |
|---|---:|---:|---:|---:|
| sqlite | 25000×768 | 1 | 276.206 | 167.892 |
| sqlite | 25000×768 | 4 | 236.812 | 356.603 |
| sqlite | 100000×2 | 4 | 424.798 | 761.148 |
| sqlite | 1024×4096 | 1 | 57.585 | 61.288 |
| postgres | 25000×768 | 1 | 220.393 | 80.388 |
| postgres | 25000×768 | 4 | 247.370 | 175.019 |
| postgres | 100000×2 | 4 | 119.449 | 233.036 |
| postgres | 1024×4096 | 1 | 45.388 | 6.200 |

At 100000×2 the legacy kernel actually transferred 100000 vectors/800000 bytes
per query while native returned 20 metadata winners and zero stored-vector bytes.
At 25000×768 the old isolated transfer was 76800000 bytes/query. There is no
100000-chunk supported production promise.

On **both real backend publication APIs**, actual publication at 25000×2 and 100000×2
returned typed `projection_capacity`; 8192×4096 also returned it under unchanged
64 MiB logical projection bounds. These six observations have samples 0 and no
supported query timing. The 8192×768 fixtures fit both bytes and coverage.
For each backend the isolated estimate rejected 8192×4096, 25000×4096,
100000×768 and 100000×4096 (each 1/4 windows), before vectors/DB allocation.
For example 100000×4096 estimated 9881600000 bytes versus 536870912 allowed.
The 1024×4096 isolated fixture supplied a bounded actual high-dimensional run.
These explicit resource outcomes are not timing extrapolations or successful
production-capacity claims. No required case failed silently.

## Correctness and accepted arithmetic differences

Every native sample fixture passed exhaustive native scalar enumeration with
independent Go source/page/ordinal aggregation, separately from true float64
cosine over the original float32 values. The unchanged check is
`epsilon(d)=8*gamma(d+2)+8*u`, `u=2^-24`, `gamma(n)=n*u/(1-n*u)`; this is a
numerical check, never a production ordering epsilon. Actual maximum errors are
in JSON: across the measured native fixtures 2.55914505e-7 (SQLite) and
1.17177782e-7 (PG), each passing its own dimension-dependent bound. Existing
independently accepted oracles also cover dimensions
1/2000/2001/4096, the explicit near-unit A/B cosine reversal, crowding, later
windows, exact/close ties and reverse insertion.

The measured 8192×2/four-window/source 50 ordering changed, legitimately:
legacy both backends `[p000086,p000072,p000114,p000006,p000090]`;
SQLite native `[p000086,p000072,p000006,p000114,p000090]`;
PG native `[p000072,p000086,p000006,p000114,p000090]`.
Native exact-oracle checks passed in each backend. Neither legacy parity nor
cross-backend identical near-match ordering is required. True native cosine,
float32 backend arithmetic and the old clamped near-unit float64 dot differ.
Random fixed vectors test arithmetic/selection, not semantic model relevance.

## Reproduction and laboratory limits

Run the commands in the JSON artifact and the
[harness instructions](../testing.md#native-vector-comparison-harness).
Go 1.26.9, Linux 6.18.26.1-microsoft-standard-WSL2, amd64, 4vCPU,
AMD Ryzen 7 PRO 7840U, 33657823232B RAM, no swap; observed available RAM 24.6GB and
temporary-filesystem space 583416229888B. This is one host, not a controlled
multi-host service benchmark. Background/OS cache effects are not eliminated.
All fixtures were temporary local SQLite files or disposable PG containers;
no live wiki, UI or external provider was accessed.

PG 16.14/pgvector 0.8.2 uses the existing pinned image
`pgvector/pgvector:0.8.2-pg16@sha256:00ba258a66dac104fd5171074a0084462a64a1369d8513f3d0a634e2f24d15bc`,
one pooled connection, shared_buffers 128MB, work_mem 4MB, jit=on, local TCP.
SQLite uses one pooled connection, WAL, foreign_keys=1, busy_timeout 5000ms;
SQLite 3.53.3/sqlite-vec v0.1.9 versions are also retained with command logs. Vectors are
seed 73 deterministic PCG/input-SHA256 normalized float32, precomputed outside
retrieval timing. External inference calls=0; local lookup/copy stays timed.
There is no real-provider end-to-end latency or model-quality claim.

Each case has a 180s context budget beginning before fixture/container/schema
setup, covering correctness/plans and repetitions as well as queries. Kernel
allocation is screened beforehand at 512 MiB estimated working set (six float32
copies plus 512B/chunk); this is an estimate, not an OS-enforced RSS quota. No
unsafe large fixture was allocated to satisfy a table. PostgreSQL container
tests are integration-tagged. Full raw Go JSON command logs and exits are
retained as `/tmp/knowl-qqz15-*`; committed JSON preserves parsed measurements
and plans without raw SQL or inlined query vectors. Failed/smoke attempts from
TASK-014 remain in its separate durable ledger and are not matrix samples.
