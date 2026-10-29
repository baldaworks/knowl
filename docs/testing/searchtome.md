# SearchTome retrieval benchmark

This benchmark compares Knowl's SQLite lexical retrieval with the actual
hybrid pipeline, using isolated canonical workspaces. The recorded SearchTome
revision is `48d3b3545950b3857dc3657fb22e4b79b8ed8df5`.
See the [recorded results and methodology](searchtome-results.md) for the E5,
EmbeddingGemma 2 and LiteRT-LM full runs, per-domain metrics and limitations.
The [pinned manifest](../../pkg/knowl/store/eval/searchtome/pinned_manifest.json)
contains the SHA-256 and record count of every corpus, train, dev and test
file at that revision. The runner verifies all 24 files and records field
names and observed types, but evaluates only test queries. The independently
loaded data contains 413 documents, 4,405 train, 1,056 dev and 6,005 test
queries across education, finance, law, medicine, natural sciences and social
sciences. It is licensed CC-BY-SA-4.0; consult the dataset's own license.

Run commands from the repository root. These instructions download data and,
when started, a model. Never point the benchmark at a real Knowl
workspace, store, or working configuration.

## Prepare data

All benchmark data and outputs live in `.searchtome-benchmark/`, which is
Git-ignored. The runner refuses symlinked benchmark/data directories and
requires a new output directory. Create a dedicated data directory and download
the pinned revision (about 6.8 MB of JSONL):

```sh
set -eu
if [ ! -e .searchtome-benchmark ] && [ ! -L .searchtome-benchmark ]; then
  mkdir .searchtome-benchmark
fi
test -d .searchtome-benchmark
test ! -L .searchtome-benchmark
mkdir .searchtome-benchmark/dataset
revision=48d3b3545950b3857dc3657fb22e4b79b8ed8df5
for domain in education finance law medicine natural_sciences social_sciences; do
  mkdir ".searchtome-benchmark/dataset/$domain"
  for split in corpus train dev test; do
    curl --fail --location --output ".searchtome-benchmark/dataset/$domain/$split.jsonl" \
      "https://huggingface.co/datasets/meghanadh/SearchTome/resolve/$revision/$domain/$split.jsonl"
  done
done
```

Only use these paths for benchmark data. The runner checks hashes rather than
trusting the directory name; any missing, changed or corrupted file stops the
run. The manifest identifies a fixed revision, not a live dataset update.

## Local embeddings

The reference configuration uses the same TEI image and model as
[the project's local embedding configuration](../search.md#local-embedding-server):
`intfloat/multilingual-e5-base`, revision
`d128750597153bb5987e10b1c3493a34e5a4502a`, 768 dimensions, `query: `
and `passage: ` prefixes, float32 mean pooling, no automatic truncation, and
`lexical` failure policy. No endpoint credential is recorded. The runner
accepts only loopback HTTP endpoints without credentials or query parameters.
It checks TEI `/info` for the model, SHA, version, dtype and truncation setting
before sending corpus text. The normal embedding client checks the model and
768-dimensional vectors on projection. The runner sends at most four inputs
per embedding request to respect the reference TEI concurrency limit. TEI metadata is not a cryptographic
attestation of the actual weights.

One option is this standalone local instance. It uses a cache inside the benchmark directory
and the pinned image from `deploy/sidecar/embeddings.compose.yaml`:

```sh
set -eu
test -d .searchtome-benchmark
test ! -L .searchtome-benchmark
if [ ! -e .searchtome-benchmark/tei-cache ] && [ ! -L .searchtome-benchmark/tei-cache ]; then
  mkdir .searchtome-benchmark/tei-cache
fi
test -d .searchtome-benchmark/tei-cache
test ! -L .searchtome-benchmark/tei-cache
docker run --rm --name knowl-searchtome-tei \
  --cpus 2 --memory 4g -p 127.0.0.1:8080:80 \
  -e AUTO_TRUNCATE=false -e RAYON_NUM_THREADS=2 \
  -e OMP_NUM_THREADS=2 -e MKL_NUM_THREADS=2 \
  -v "$PWD/.searchtome-benchmark/tei-cache:/data" \
  ghcr.io/huggingface/text-embeddings-inference:cpu-1.9.0@sha256:bc7ad262695df5b7875b0c9c702deb8e9df3953bdf22c3d6068d9c0429b7b3f3 \
  --json-output --model-id intfloat/multilingual-e5-base \
  --revision d128750597153bb5987e10b1c3493a34e5a4502a \
  --hostname 0.0.0.0 --served-model-name intfloat/multilingual-e5-base \
  --dtype float32 --pooling mean --tokenization-workers 2 \
  --max-concurrent-requests 4 --max-client-batch-size 16 \
  --max-batch-requests 8 --max-batch-tokens 8192 --payload-limit 65536
```

This command runs in the foreground. Stop that named container when finished.
For production deployment choices and the reference compose service, see
`deploy/sidecar/embeddings.compose.yaml` and `docs/search.md`.

## Test and run

The deterministic fixture uses a controlled embedding provider and is not a
real-model quality measurement:

```sh
go test ./pkg/knowl/store/eval/searchtome ./cmd/knowl-searchtome
```

Once the reference TEI is ready, a small smoke run uses three
test queries per domain. It still builds a full projection for each domain and
explicitly marks other queries as `smoke_limit` exclusions:

```sh
go run ./cmd/knowl-searchtome --dataset dataset --run smoke-01 \
  --tei http://127.0.0.1:8080/v1/embeddings --limit 3
```

After smoke succeeds, use a fresh `--run` name for the potentially long full
run and monitor until completion:

```sh
go run ./cmd/knowl-searchtome --dataset dataset --run full-01 \
  --tei http://127.0.0.1:8080/v1/embeddings
```

Each domain has a separate canonical workspace and two new SQLite stores. Both
modes use the same published snapshot, test queries, scope, all-source filter,
`k=3`, and no warm-up. Projection failure, capacity overflow, or a non-ready
hybrid projection stops the run before its measurements; degraded and failed
queries are counted and excluded from successful hybrid metrics. Query errors,
missing or ambiguous gold titles and duplicate query IDs are reported rather
than guessed. Observations contain no raw query or upstream error bodies.

A successful run writes `report.json`, `report.md`, and finally `complete.json`
containing both report hashes in its run directory. Without the final marker,
the run is incomplete. JSON retains per-query observations for recomputation;
Markdown provides domain, macro, micro, paired deltas and exclusions. Latency
percentiles use nearest rank; macro latency is the mean of domain percentiles.
The paired delta uses only queries that succeeded in both modes. The result
includes commit, dirty state, runtime, CPU, dataset and model identities,
projection timing and counts. A chunk count may be unavailable when no hybrid
query succeeds. Check the report for error counts and limitations before
interpreting its denominators. No guarantee of bitwise ranking parity across
backends or inference implementations is made.

To validate the completion marker and independently recompute micro metrics,
choose an existing run directory and decode the JSON (the script reads but does
not modify the artifacts):

```sh
python3 - .searchtome-benchmark/full-01 <<'PY'
import hashlib, json, math, pathlib, sys

run = pathlib.Path(sys.argv[1])
marker = json.loads((run / 'complete.json').read_text())
if set(marker) != {'report.json', 'report.md'}:
    raise SystemExit('invalid completion marker')
for name, expected in marker.items():
    actual = hashlib.sha256((run / name).read_bytes()).hexdigest()
    if actual != expected:
        raise SystemExit(f'checksum mismatch: {name}')
report = json.loads((run / 'report.json').read_text())
for mode in ('lexical', 'hybrid'):
    observations = report[mode]
    eligible = [row for row in observations if not row.get('exclusion')]
    n = len(eligible)
    if not n:
        raise SystemExit(f'no eligible {mode} queries')
    r1 = sum(row['gold_rank'] == 1 for row in eligible) / n
    r3 = sum(0 < row['gold_rank'] <= 3 for row in eligible) / n
    ndcg = sum(1 / math.log2(row['gold_rank'] + 1)
               if 0 < row['gold_rank'] <= 3 else 0 for row in eligible) / n
    print(f'{mode}: eligible={n}, excluded={len(observations)-n}, '
          f'R@1={r1:.4f}, R@3={r3:.4f}, nDCG@3={ndcg:.4f}')
PY
```

Use the actual fresh run name in place of `full-01`. Review the report's
per-domain counts and exclusions before comparing results. If the marker is
absent or a checksum differs, treat the run as incomplete.

A SearchTome score from this public six-book dataset is not directly comparable
to the published STAIR 82.6% Recall@1 without an identical corpus and protocol.

### EmbeddingGemma 2 experiment

The optional `--model gemma2` profile uses `google/embeddinggemma-2`, revision
`f9972ac22e696a105c482202646979e33e4b5a1e`, with 768 dimensions. It uses
`task: search result | query: ` for queries and `title: none | text: ` for
passages. Knowl's fixed passage prefix does not inject the individual page
title; the model's `title: none` form is used instead. This is a limitation
when comparing the model with a title-aware implementation.

TEI 1.9 does not support this model architecture. The local
`cmd/knowl-searchtome/gemma2_server.py` adapter serves the official model with
Sentence Transformers 6.1.0, CPU float32, on loopback port 8081. It exposes
the OpenAI-compatible embeddings interface and rejects overlong inputs rather
than silently truncating them. It does not perform search. Install dependencies
in an isolated environment and keep Hugging Face, pip and temporary caches
under `.searchtome-benchmark/`. The pinned requirements record the exact CPU
runtime used for this experiment. Create the environment and install it:

```sh
mkdir -p .searchtome-benchmark/tmp
python3 -m venv .searchtome-benchmark/gemma2-venv
TMPDIR="$PWD/.searchtome-benchmark/tmp" \
PIP_CACHE_DIR="$PWD/.searchtome-benchmark/gemma2-pip-cache" \
  .searchtome-benchmark/gemma2-venv/bin/pip install \
  --extra-index-url https://download.pytorch.org/whl/cpu \
  -r cmd/knowl-searchtome/gemma2-requirements.txt
```

Start the server in a terminal and leave it running. Its first start downloads
the model weights into the benchmark cache; subsequent runs can use
`HF_HUB_OFFLINE=1` after the download succeeds. The model cache occupied about
1.5 GiB in the recorded run; budget additional space for the Python environment
and projections.

```sh
HF_HOME="$PWD/.searchtome-benchmark/gemma2-hf" \
XDG_CACHE_HOME="$PWD/.searchtome-benchmark/gemma2-xdg" \
TMPDIR="$PWD/.searchtome-benchmark/tmp" \
PYTHONDONTWRITEBYTECODE=1 TOKENIZERS_PARALLELISM=false \
.searchtome-benchmark/gemma2-venv/bin/python cmd/knowl-searchtome/gemma2_server.py
```

In a second terminal, run smoke and then a full run, each with a new name:

```sh
go run ./cmd/knowl-searchtome --dataset dataset --run gemma2-smoke \
  --model gemma2 --tei http://127.0.0.1:8081/v1/embeddings --limit 3

go run ./cmd/knowl-searchtome --dataset dataset --run gemma2-full \
  --model gemma2 --tei http://127.0.0.1:8081/v1/embeddings
```

After the run, stop the server with Ctrl-C in its terminal. The recorded
Gemma smoke took about 39 minutes to build all six projections, and the full
run also evaluates all 6,005 queries. Times vary by machine; the server has a
two-thread CPU setting. The earlier E5 reports stay in their own run directories.

The server reports its declared pinned revision, but an OpenAI-compatible
endpoint alone cannot attest the model weights. Inspect the local server and
model cache when reproducing the run. Existing E5 reports are never reused or
overwritten. Inference backend and latency differ from the E5 TEI run.

### LiteRT-LM EmbeddingGemma 2 experiment

The `--model litert` profile evaluates the official text-only 270M LiteRT-LM
model on CPU with two threads, int4 QAT weights and 768-dimensional normalized
embeddings. Its task prefixes are the same as the preceding float32 Gemma
experiment, to make this particular comparison easier to interpret; Google's
LiteRT-LM guide also documents other task-specific prefixes. Model quantization,
inference runtime and numeric behavior still differ from the float32 run.

The adapter verifies the model file's SHA-256 before loading it, rejects input
overflow instead of truncating, and exposes a loopback-only OpenAI-compatible
embedding endpoint. It calls LiteRT-LM's embedding API and does not perform
retrieval. The endpoint's metadata can be verified by inspecting the local
adapter and model file; an HTTP response alone is not a weight attestation.

From the repository root, prepare an isolated environment and download the
exact model revision. The file is about 165 MB:

```sh
test -d .searchtome-benchmark && test ! -L .searchtome-benchmark
python3 -m venv .searchtome-benchmark/litert-venv
TMPDIR="$PWD/.searchtome-benchmark/tmp" \
PIP_CACHE_DIR="$PWD/.searchtome-benchmark/litert-pip-cache" \
  .searchtome-benchmark/litert-venv/bin/pip install \
  -r cmd/knowl-searchtome/litert-requirements.txt
mkdir .searchtome-benchmark/litert-model
curl --fail --location \
  --output .searchtome-benchmark/litert-model/embeddinggemma-2-text-270m.litertlm \
  https://huggingface.co/litert-community/embeddinggemma-2-text-270m-litert-lm/resolve/9be6e8b90982095dc05c2bd162e4b954ee4dbac7/embeddinggemma-2-text-270m.litertlm
sha256sum .searchtome-benchmark/litert-model/embeddinggemma-2-text-270m.litertlm
```

The expected SHA-256 is
`2d079ee2f6f066b1f368e8d7c819f55214eaef1d0513b312321901f30ab286fb`.
Create or reuse the local cache and start the adapter in one terminal:

```sh
if [ ! -e .searchtome-benchmark/litert-cache ] && [ ! -L .searchtome-benchmark/litert-cache ]; then
  mkdir .searchtome-benchmark/litert-cache
fi
test -d .searchtome-benchmark/litert-cache && test ! -L .searchtome-benchmark/litert-cache
TMPDIR="$PWD/.searchtome-benchmark/tmp" \
XDG_CACHE_HOME="$PWD/.searchtome-benchmark/litert-cache" \
PYTHONDONTWRITEBYTECODE=1 \
  .searchtome-benchmark/litert-venv/bin/python cmd/knowl-searchtome/litert_server.py
```

In a second terminal, use fresh run names:

```sh
go run ./cmd/knowl-searchtome --dataset dataset --run litert-smoke \
  --model litert --tei http://127.0.0.1:8082/v1/embeddings --limit 3
go run ./cmd/knowl-searchtome --dataset dataset --run litert-full \
  --model litert --tei http://127.0.0.1:8082/v1/embeddings
```

Stop the adapter with Ctrl-C after the run. Run the checksum and metric
verification shown above on `litert-full` and retain its output separately.

Check `git status --short` before any commit: downloaded data, model cache,
stores and run results must stay out of Git. To reclaim space, inspect the
exact benchmark-owned path and remove only the intended run, dataset or model
cache; never delete `.beads/` or an existing user workspace.
