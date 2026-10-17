# Native vector retrieval: local comparative results

## Trusted publication comparison — 2026-10-17

Results are mixed. At 8192 chunks, 768 dimensions, one window and 50% source
eligibility, trusted native SQLite measured **68.132ms versus 426.163ms** for
the refreshed actual Go baseline. PostgreSQL measured **186.948ms versus
157.079ms**, a remaining regression. With all sources eligible, PostgreSQL
measured 59.517ms versus 146.657ms. SQLite's low-dimension four-window case also
remains slower than Go. These observations support no general latency claim or
new SLO. The original much slower v1.3 SQL measurements remain visible below.

All current pairs ran strictly sequentially on the same host, with **n=3 warm
queries per case**. Table cells show median (observed min–max), in milliseconds.
The historical SQL column is dated evidence, not a contemporaneous third run;
do not infer a causal speedup factor from it. OS/cache/background effects are
not eliminated, and three samples do not establish reliable tails. The raw
compatibility `P95NS` field is only nearest-rank=max of three.

| Backend | dims/windows/source % | Refreshed Go ms (min–max) | Trusted native ms (min–max) | Historical v1.3 native median ms |
|---|---|---:|---:|---:|
| sqlite | 2/1/50 | 67.593 (66.382–69.721) | 38.830 (38.455–39.545) | 1174.389 |
| sqlite | 2/4/50 | 69.634 (69.594–70.957) | 73.098 (69.270–73.840) | 1183.050 |
| sqlite | 768/1/50 | 426.163 (425.573–436.138) | 68.132 (64.332–69.368) | 2929.833 |
| sqlite | 768/4/50 | 452.554 (438.680–465.684) | 138.758 (137.375–156.208) | 3102.299 |
| sqlite | 768/1/100 | 462.949 (461.335–467.712) | 103.451 (101.413–112.053) | 3077.691 |
| postgres | 2/1/50 | 62.210 (60.539–62.291) | 168.249 (167.172–171.770) | 198.676 |
| postgres | 2/4/50 | 66.439 (63.545–67.300) | 176.600 (173.507–177.024) | 210.722 |
| postgres | 768/1/50 | 157.079 (155.512–158.211) | 186.948 (178.318–188.651) | 807.677 |
| postgres | 768/4/50 | 187.183 (184.219–194.752) | 214.689 (213.777–217.588) | 825.127 |
| postgres | 768/1/100 | 146.657 (142.780–147.521) | 59.517 (57.207–61.740) | 739.054 |

The production revision is `643933d61e82ff5d396ae5f3db3a13d57ba1c840`;
actual legacy production remains `dec6249b68a27435217e29bef9a5669fee068cb0`.
The four runtime/fixture/oracle harness files were byte-identical in both
checkouts; their SHA256s, exact commands, toolchain/backend/settings/hardware,
raw-log checksums, all 20 observations and 20 actual structured plans are
appended under `TrustRefinement` in [native-vectors.json](native-vectors.json).
Top-level observations/plans retain their original historical payloads.
The listed harness adds measurement attribution only; fixture seed 73, original
prepared vectors, 128 pages, page limit 5 and channel limit 20 are unchanged.
Every current pair matches the historical fixture checksum.

Knowl-controlled publication/reuse/recovery still fully validates the complete
projection before existing atomic ready publication; page changes invalidate
incompatible state. Ordinary search/context/health/startup/lint use bounded
scalar readiness/snapshot/model admission, with original-evidence validation
where consumed. Per-read auditing of arbitrary nonselected SQL corruption is
outside the approved application-owned trust boundary. No schema, cache,
generation, setting, provider, scoring rewrite or capacity increase was added.

In these search fixtures, every native query transferred two scalar admission
rows: 276 logical result-cell bytes on SQLite, 259 on PostgreSQL. It transferred
zero coverage-manifest bytes and zero stored vectors, with 20 dense candidates,
5 evidence rows and 32 total driver rows including page fetches. Full-scope
`ScannedChunks=8192` comes from admitted published state; it is not a fresh
integrity audit or the candidate count. The instrumented bytecode/catalog-root
and typed PostgreSQL-plan tests also cover increasing populations and
search/context/health caller paths; actual startup/lint composition tests retain
repair/failure behavior. Exhaustive eligible native cosine remains necessary.

For 768/one-window/source 50, current Go allocation volume is
79.904→1.684 MiB/query on SQLite and
79.974→1.764 MiB/query on PostgreSQL.
These are B/op, **not RSS**. The old pipeline transfers 8192 stored vectors,
24 MiB/query at 768 dimensions. Native queries still transfer current page and
evidence bodies; zero stored vectors does not mean zero result bytes.

For that same fixture, mean scalar SQL row-consumption intervals were
0.132ms/1.101ms
(SQLite/PostgreSQL), and ranking intervals
61.867ms/173.686ms.
They include client consumption, are not pure server CPU, and cannot be added
as independent median phases. The refined scalar counter now includes the fixed
metadata row; historical counters keep their original definition. Legacy scalar
classification also includes page-coverage rows. The observed source-filter
contrast alone does not establish a causal hotspot.

Publication and maintenance are outside query timers. Each case records one
full `Rebuild` call as `PublicationNS` and one same-snapshot `Project` call as
`ReuseNS`; these include lexical work, full validation and writes, not isolated
validator microbenchmarks. For 768/one-window/source 50, native Rebuild/Project
observed 1.439/2.305s on SQLite and
6.926/8.507s on PostgreSQL.
These are single setup observations, not latency distributions.

All ten native cases passed exhaustive native/page aggregation and independent
float64 true-cosine checks with the unchanged Design bound. Observed maximum
errors were 2.10326643e-7
(SQLite) and 9.97581830e-8
(PostgreSQL); accepted native/legacy and backend near-match ordering differences
remain. A separate earlier frozen-harness SQLite 8192×2/one-window/source 50
sample measured median 38.310ms, range
36.936–38.727ms, n=3;
it is recorded as `TrustRefinement.Reproduction`, not pooled with the matrix.

Original 25000/100000 capacity rejections, isolated kernels and high-dimensional
resource outcomes are **retained, not rerun**, at the original `6d4ee134...`
revision. Review confirmed the kernel/scoring/fixture/oracle and production
limits unchanged; TASK-018 reran supported bounds, oracles, publication/reuse,
CAS/invalidation, failure/cancellation, CGO-free, PostgreSQL and race coverage.
These old larger-case results do not become new production-capacity claims.
The host remains Go 1.26.9, Linux/WSL2, 4vCPU Ryzen 7 PRO 7840U, 33,657,823,232B
RAM, no swap; SQLite 3.53.3/sqlite-vec 0.1.9 and PG 16.14/pgvector 0.8.2 use the
recorded local/disposable settings. External inference calls were zero.

The following sections preserve the original v1.3 analysis and measurements.

## Historical v1.3: Outcome: material supported-query latency regression

The native implementation transfers no stored vectors into Go, but **full query
latency regressed on both backends** in this bounded laboratory. At 8192 chunks,
768 dimensions, one window and 50% source eligibility, the measured native median
was 2.930s versus 0.365s previously on SQLite (8.02×), and 0.808s versus 0.141s on
PostgreSQL (5.75×). This is a material performance regression at a supported
size, not a data-integrity failure. It must remain visible before any publishing
or merge decision. Do not advertise a latency improvement. At that revision, optimizing validation was separate,
unimplemented work; those measurements did not authorize weakening integrity
checks. The later approved application-owned trust refinement is reported above.

The complete typed observations, actual wall samples, allocation metrics,
transfer counts, ordered result IDs and parsed DB plans are in
[native-vectors.json](native-vectors.json). No production bound was raised.

## Historical v1.3: Paired full-query measurements

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

## Historical v1.3: Isolated kernels and unsupported capacity

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

## Historical v1.3: Correctness and accepted arithmetic differences

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

## Historical v1.3: Reproduction and laboratory limits

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
