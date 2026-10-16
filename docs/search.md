# Search your wiki

Knowl uses lexical search by default. It matches indexed page text, titles,
headings and tags. With embeddings enabled, hybrid search combines lexical and
semantic candidates and returns evidence from the same published wiki. An
embedding model helps match related wording; it does not generate answers or
maintain pages.

Semantic page scoring executes exhaustively inside the database: bundled
CGO-free sqlite-vec for SQLite, or operator-provisioned pgvector for PostgreSQL.
Each eligible page receives its maximum **native true cosine** across every
query window and chunk before the page candidate limit is applied. Equal native
scores use the lowest winning chunk ordinal, then bytewise page identity;
close unequal scores keep native database order, without an ordering epsilon.
This can change page order or evidence from the old near-unit Go dot product,
and between backends. Lexical ordering and bounded fusion remain unchanged.

Native queries validate the entire bounded scope, including nonwinning and
unselected-source chunks, without transferring stored vectors into Go. This
includes model-space/dimension compatibility: incompatible active-scope rows
cannot be silently filtered out to report valid dense retrieval. This
does **not** guarantee lower latency: [actual paired measurements](testing/native-vectors.md)
show material full-query regressions at 8192 chunks despite lower allocation
volume. Separate isolated kernels are not production throughput.

Search configuration is independent of how you run Knowl: project-local CLI,
MCP/HTTP service and Go applications share the same retrieval behavior. The
wiki maintainer remains your separately configured [agent](agents.md).

Choose an external OpenAI-compatible embedding API or a [local TEI server](#local-embedding-server).
For an external API, replace the example endpoint, model, revision, dimensions
and prefixes with the values required by that deployment. For local TEI, use
the pinned reference configuration below. See [measurements](testing.md#real-cpu-embedding-quality-gate)
for evaluated quality and resource use.

## Configure an embedding API

Embeddings are off by default. Disabled configuration creates no embedding
client, reads no embedding credential and makes no model/readiness request.
The maintainer selected by `knowl.provider` remains a separate requirement.

Merge this opt-in fragment into your existing configuration, preserving the
maintainer, sources, storage and authentication. The endpoint below addresses
the local TEI service on its private Compose network:

```yaml
knowl:
  embeddings:
    enabled: true
    endpoint: http://tei:80/v1/embeddings
    model: intfloat/multilingual-e5-base
    revision: d128750597153bb5987e10b1c3493a34e5a4502a
    dimensions: 768
    query_prefix: 'query: '
    passage_prefix: 'passage: '
    failure_policy: lexical
```

`endpoint` is the full URL for one OpenAI-compatible float embedding API. Use
`api_key_env: YOUR_EMBEDDING_KEY` to read an optional bearer credential from the
process environment. It names the variable, not its value; a missing configured
credential fails startup. Enabled configuration requires model/revision and
1–4,096 dimensions. Model/revision/prefix fields are bounded at 256 UTF-8 bytes;
the endpoint at 2,048. Empty prefixes are supported for models that require none.
Userinfo and fragments in endpoints are rejected. HTTPS uses normal TLS;
private HTTP is supported. Redirects and inference retries are disabled.

The operator must keep the declared immutable revision aligned with the served
weights. The response model name and vector dimension are checked; the generic
API cannot attest weight identity. Change the declared revision and rebuild
when weights change. The pinned reference is
[E5-base](https://huggingface.co/intfloat/multilingual-e5-base), served by the
[local TEI server](#local-embedding-server) with mean float32 output,
required prefixes and server truncation disabled.

## Coverage, limits and recovery

| Setting or fixed bound | Behavior |
| --- | --- |
| `failure_policy: lexical` | Classified embedding failure returns lexical evidence with `degraded` mode and a safe reason |
| `failure_policy: strict` | Missing/unavailable/incompatible dense retrieval returns a typed failure |
| Request | At most 16 inputs, 2,048 UTF-8 bytes each including prefix, 64 KiB serialized body |
| Response | At most 1 MiB; complete unique indices, exact model/dimensions, finite nonzero normalized vectors |
| Inference | 10-second request bound or earlier caller deadline; four concurrent client requests, no hidden retry |
| Semantic input | Markdown/OKF pages: heading sections, up to 384 runes per input, split at block boundaries; queries and source signals: up to four 384-rune windows with 64-rune overlap |
| Projection | At most 8,192 chunks, 1 MiB of page coverage metadata and 64 MiB total per scope; 15-minute rebuild or earlier caller deadline |

The 64 MiB bound accounts for logical float32 vector payload and bounded metadata,
not extra PostgreSQL native-storage headers. Dimensions 1–4096 are supported by
exact scalar retrieval; no ANN index, chunk shortlist or increased capacity is
implied. `ScannedChunks` counts the successfully validated complete scope before
source filtering, not only SQL candidates. Candidates/evidence remain bounded
by `min(100,max(20,4*k))`; complete scalar/integrity database work is still needed.

Rune limits are not tokenizer limits. The service must reject token overflow
rather than silently truncate; the pinned TEI reports `input_limit`. Chunk/rune
coverage omissions for bounded queries/source signals appear in the Go report;
ready page projections have zero omissions. Lexical full-field indexing and
maintenance's complete-page/request limits still apply. Larger corpora require a
separately evaluated capacity design; these bounds are not throughput promises.

HTTP/MCP results optionally include, for example:

```json
{"retrieval":{"effective":"degraded","reason":"unavailable"}}
```

Effective modes are `lexical`, `hybrid`, `degraded`, and `failed`. Go
`QueryResult.Retrieval`, `IngestResult.Retrieval` and durable
`Operation.Retrieval` additionally carry requested mode, model-space prefix,
candidate/scanned-chunk counts and omitted coverage. Maintenance records its
selection report before maintainer inference. `Operation.RetrievalAttempt`
identifies its originating work attempt; terminal/legacy replay does not invent
or replace historical reports. Reports omit text, endpoint URLs, credentials and
upstream error bodies. Public transport exposes only effective mode and reason.

A projection refresh after source synchronization or a canonical edit updates
lexical search, links and source provenance. For Markdown and OKF pages, dense
inputs follow document sections and their heading ancestry; long sections split
at block boundaries, and an oversized block splits into bounded pieces. The
title and semantic metadata accompany the page inputs; dense citations retain
the original document text. The projection reuses a prior vector only when its
model space, complete prior state and prepared input match. An unchanged
snapshot or a source-only revision therefore needs no new page embeddings;
changed sections require new embeddings.

An explicit rebuild replaces lexical state and computes every page vector again
in batches of at most 16 without holding SQL locks during inference. Dense
publication checks the current snapshot and complete page coverage; a
conflicting update discards a stale build. A classified failure saves degraded
state even under strict policy. With lexical fallback, startup may be ready
while semantic retrieval is degraded; `/readyz` alone is not a hybrid guarantee.
Read the retrieval status. Invalid input and caller cancellation never return
successful fallback.

Queries never trigger a rebuild or download a model. After repairing a degraded
service, restart Knowl to retry the projection once during startup. Embedded
applications can call `knowl.RebuildProjection(ctx, config, snapshot)` explicitly.
Changing model/revision/dimensions/prefixes requires fresh embeddings for the
new model space and changes maintenance identity. The structured page input
version also changes model-space identity: existing dense state is rebuilt once
on startup before later projections can reuse unchanged inputs. A strict
projection error can occur after a canonical commit: repair the derived
projection, preserving the committed facts and raw evidence.

Migration 16 originally added disposable vector state/reports, and 21 coverage.
The native transition is numbered Goose SQL migration 26 on both backends: it adds a format
marker and invalidates derived dense state, preserving canonical/raw/source and
operation data. SQLite keeps portable little-endian float32 BLOBs with the
bundled native capability, including `CGO_ENABLED=0`; no extension installation,
shared-library load path or external model process is needed for its SQL math.
Inference still uses the configured provider.

Stop old writers, migrate with the matching binary, rebuild, verify actual
semantic retrieval and only then resume. Format/preprocessing changes require
fresh dense publication; do not treat legacy state as ready. PostgreSQL's base
stream stays extension-free and its additional optional Goose SQL migration 1 converts
derived vectors to native pgvector only when capability/authority permit.
See [PostgreSQL deployment and recovery](postgres.md) for two histories,
provision/reopen/rebuild, runtime grants, corrupt empty history and optional-first
Down. All new application schema changes are Goose SQL; normal Goose-on-Open
and historical Go migration 19 remain, not a blanket ban on migration DDL at startup.
Down invalidates derived native state; pair schema and compatible binary and
rebuild before resuming. Disabling embeddings retains default lexical behavior and
ignores derived vectors. See [pending-operation recovery](operations.md#upgrading-pending-operations)
for generation changes.

## Local embedding server

Complete the [service setup](service.md#build-and-run), including its three
environment variables. From this checkout, opt in with the checked-in
[embedding config](../deploy/sidecar/embeddings.yaml) and
[Compose overlay](../deploy/sidecar/embeddings.compose.yaml):

```bash
docker compose -f deploy/sidecar/compose.yaml \
  -f deploy/sidecar/embeddings.compose.yaml up --build -d
```

The base service profile remains lexical-only. The overlay mounts
`embeddings.yaml` as a **complete replacement configuration**, preserving the
base profile's OpenAI maintainer, engineering source, storage and operator token.
It does not merge individual Knowl settings. If you customized the base config,
carry those settings into your complete hybrid config before switching profiles.
Use the current checkout; the historical v0.5.0 quickstart is a separate recipe.

The private `tei` service runs the pinned linux/amd64 CPU TEI 1.9.0 image and
E5-base revision `d128750597153bb5987e10b1c3493a34e5a4502a`, returning 768
normalized dimensions. The exact image digest is in the overlay. It publishes no
host port, uses no GPU and shares Knowl's private Compose network. Mean float32,
required query/passage prefixes, no second server prompt and disabled auto
truncation match the client contract. Other machines/profiles need their own
runtime and quality validation.

First startup explicitly downloads the model into the `tei-model-cache` named
volume. Persist that volume for offline restarts. The reference allocates 2 CPU
cores and 4 GiB to TEI with a 10-minute cold-start health allowance. Observed cold
readiness was 194.5 seconds, cached restart 6.6 seconds, model cache 1,075 MiB and
idle memory about 1.91 GiB on the recorded test machine. These are fixed-profile
measurements, not hardware minima or latency guarantees. See the
[actual quality record](testing.md#real-cpu-embedding-quality-gate).

```bash
docker compose -f deploy/sidecar/compose.yaml \
  -f deploy/sidecar/embeddings.compose.yaml ps tei
```

The example permits Knowl to start during TEI cold loading using explicit
lexical fallback. Check retrieval mode as well as `/readyz`. For
`failure_policy: strict`, wait for TEI health and model loading before starting
Knowl. After an unavailable/degraded startup, restart Knowl once TEI is healthy
to rebuild dense state; queries do not repair it automatically. Stopping the
stack with `down` preserves its named volumes.

For project-local Knowl, run TEI as a separate service and publish its port only
to host loopback, then set `knowl.embeddings.endpoint` to that host URL ending in
`/v1/embeddings`. The `tei` hostname above resolves inside the Compose network,
not from a host process. Keep the same model, revision, dimensions and prefixes
on the client and server.
