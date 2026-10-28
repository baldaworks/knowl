# SearchTome retrieval evaluation

This page records two local measurements of the checked-in SearchTome runner.
[The runbook](searchtome.md) describes how to reproduce them. The numbers are
observations for this dataset and machine, not production latency guarantees.

## Comparison

The table reports percentages over the **same 5,980 eligible test queries**.
Latency is per query, in milliseconds, using the nearest-rank percentile. The
lexical latency is from the E5 run; the separate Gemma run's lexical p50/p95
were 10.52/17.13 ms.

| System | R@1 | R@3 | nDCG@3 | p50 ms | p95 ms |
| --- | ---: | ---: | ---: | ---: | ---: |
| Knowl lexical | 58.51 | 78.95 | 70.60 | 10.79 | 17.36 |
| Knowl hybrid, multilingual-e5-base | 71.37 | 88.34 | 81.47 | 82.23 | 114.64 |
| Knowl hybrid, EmbeddingGemma 2 | 73.06 | 89.82 | 83.00 | 212.57 | 264.17 |
| STAIR, published reference | 82.6 | 90.8 | 87.5 | — | — |

Gemma improved micro R@1 by 1.69 percentage points, R@3 by 1.47 points and
nDCG@3 by 1.54 points against E5. Its observed p50 was about 2.59 times E5's.
The latency comparison includes different local inference servers and runtimes,
not just a change in model. The two embedding models also have different
instruction prefixes and chunk counts.

The STAIR row is a **separate, non-comparable reference** from [table 4 of the
paper](https://arxiv.org/html/2609.03874). That paper describes 18 books across
six domains and reports its own Avg. The public dataset revision tested here
contains six books, one per domain; its query population, corpus and protocol
are not identical. Do not interpret the difference as a head-to-head result.

## Results by domain

Each cell gives R@1 / R@3 / nDCG@3 as percentages. All three systems in each
row use the same eligible queries. The micro row weights queries equally;
macro is the unweighted mean of the six domain metrics, not an average over
all queries.

| Domain | Documents | Test queries | Eligible | Lexical | E5 hybrid | Gemma 2 hybrid |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Education | 8 | 437 | 435 | 47.36 / 76.32 / 64.16 | 65.98 / 90.34 / 80.27 | 65.98 / 92.64 / 81.60 |
| Finance | 86 | 1,214 | 1,214 | 46.62 / 72.49 / 61.96 | 59.56 / 80.97 / 72.25 | 61.70 / 82.45 / 73.96 |
| Law | 47 | 820 | 808 | 73.51 / 88.61 / 82.57 | 85.15 / 94.80 / 90.92 | 85.15 / 95.05 / 91.06 |
| Medicine | 121 | 1,996 | 1,993 | 64.07 / 82.54 / 75.02 | 77.87 / 92.47 / 86.64 | 78.98 / 93.48 / 87.69 |
| Natural sciences | 34 | 579 | 573 | 69.81 / 85.51 / 79.19 | 80.45 / 94.07 / 88.65 | 80.63 / 95.29 / 89.37 |
| Social sciences | 117 | 959 | 957 | 47.65 / 68.76 / 60.02 | 58.20 / 79.31 / 70.66 | 63.64 / 82.55 / 74.75 |
| **Macro** | 413 | 6,005 | 5,980 | 58.17 / 79.04 / 70.49 | 71.20 / 88.66 / 81.56 | 72.68 / 90.24 / 83.07 |
| **Micro** | 413 | 6,005 | 5,980 | 58.51 / 78.95 / 70.60 | 71.37 / 88.34 / 81.47 | 73.06 / 89.82 / 83.00 |

All 6,005 queries were attempted per mode. The same 25 queries returned typed
`invalid_query` in both runs and both modes: two education, 12 law, three
medicine, six natural sciences and two social sciences. Hybrid reported these
as `failed/invalid_input`; there were no `degraded` queries. They are excluded
from quality metrics, leaving 5,980 successful paired queries. The excluded
query IDs, scoped by domain, are:

- Education: 68, 233.
- Law: 152, 200, 203, 293, 432, 516, 586, 601, 653, 712, 752, 771.
- Medicine: 324, 827, 1782.
- Natural sciences: 4, 6, 184, 279, 326, 332.
- Social sciences: 684, 790.

Finance has no excluded queries. The full local JSON and Markdown reports
retain the per-mode result for each ID.

## Method and environment

The dataset was `meghanadh/SearchTome`, revision
`48d3b3545950b3857dc3657fb22e4b79b8ed8df5`, licensed CC-BY-SA-4.0. Its 24
JSONL files were checked against the [pinned manifest](../../pkg/knowl/store/eval/searchtome/pinned_manifest.json):
413 corpus records, 4,405 train, 1,056 dev and 6,005 test queries. Only test
queries were evaluated. Corpus rows contain `title` and `content`; query rows
contain `query_id`, `query`, `answer` and `gold_doc_title`.

Each domain used a separate canonical workspace and SQLite database per mode.
Pages were published through Knowl's supported filesystem workflow, then
indexed through the real projection and retrieval pipeline. Both modes used
the same published pages, query order, domain scope, all-source filter, k=3
and evidence limits. Lexical had embeddings disabled; hybrid used lexical and
dense candidates, native cosine, reciprocal rank fusion and Knowl's evidence
selection. The full projection had to be ready before measurement. No retrieval
warm-up queries were run. The Gemma smoke run had an earlier neutral endpoint
probe; the full Gemma run restarted the server and used only its metadata
readiness check before projection.

A result is eligible only when retrieval succeeds in the requested mode and
the gold title maps unambiguously to a page. Recall@k is the mean of whether
that page appears in the top k; with one relevant page, nDCG@3 is
`1/log2(rank+1)` for ranks 1–3, otherwise zero. The runner records per-query
rank, top-three identities, safe retrieval status, candidate counts, latency
and exclusion; it does not record query text or upstream error bodies.

Both runs used Knowl commit `623c5f3189909f9d5deb8f13e0630e164f5f2872`
with **uncommitted benchmark changes** in a dirty tree. The commit alone does
not identify the measured harness. They ran on Linux/amd64, Go 1.26.10, SQLite,
an AMD Ryzen 7 PRO 7840U, with local CPU inference and 768-dimensional vectors.
Both used four embedding inputs per client batch, no server auto-truncation,
lexical fallback policy, and two CPU threads for inference. Endpoint-reported
metadata is not cryptographic proof of the served weights.

- E5: `intfloat/multilingual-e5-base` revision
  `d128750597153bb5987e10b1c3493a34e5a4502a`; `query: ` and `passage: `;
  float32 mean pooling on the pinned TEI 1.9.0 CPU image.
- Gemma: `google/embeddinggemma-2` revision
  `f9972ac22e696a105c482202646979e33e4b5a1e`; `task: search result | query: `
  and `title: none | text: `; float32 text-only Sentence Transformers 6.1.0
  with the local OpenAI-compatible adapter. Knowl's fixed passage prefix does
  not insert each page's actual title, so this is not a title-aware Gemma run.

Full projection build times below include local inference, and chunk counts
are model-specific. The longer Gemma prefixes change the chunking budget, so
the numbers do not represent an identical set of dense chunks.

| Domain | E5 chunks | E5 build s | Gemma chunks | Gemma build s |
| --- | ---: | ---: | ---: | ---: |
| Education | 841 | 71.98 | 885 | 196.48 |
| Finance | 1,580 | 159.90 | 1,633 | 487.68 |
| Law | 1,290 | 118.41 | 1,337 | 335.44 |
| Medicine | 2,900 | 277.80 | 3,058 | 778.60 |
| Natural sciences | 677 | 62.53 | 718 | 172.81 |
| Social sciences | 1,524 | 158.02 | 1,582 | 421.29 |
| **Total** | **8,812** | **848.64** | **9,213** | **2,392.30** |

E5 was run twice: top-three pages, gold ranks, effective modes, exclusions and
retrieval metrics matched for all 6,005 queries in both modes. Gemma was run
once in full; its 18 queries shared with the earlier smoke matched in rank and
top three. This is not a full repeatability check for Gemma.

## Evidence and limitations

The original large JSON, Markdown and database artifacts remain local under
`.searchtome-benchmark/full-02/` and `.searchtome-benchmark/gemma2-full-01/`;
they are not versioned. Their `complete.json` files bind both reports by
SHA-256:

| Run | JSON SHA-256 | Markdown SHA-256 |
| --- | --- | --- |
| E5 `full-02` | `acc997e2abcceccd8e2700b3810cd4b5d16397e5a1fc601a3be73c513f47d31a` | `9d48350d0d8b6e843117b5d88de6995dba06f61159e886545504d397cad53357` |
| Gemma `gemma2-full-01` | `6957c5c90ae356cd1ad5fcebadcec7791e535a247364bfd40807e29836b987d1` | `917f13a1c027b1d318bba0638956b19ee08a54e5c3e276cd2b849985d35ffdf9` |

The manifest contains the independent dataset checksums. If local artifacts are
unavailable, rerun the [runbook](searchtome.md); the tables above alone cannot
reconstruct per-query results.
