# Search your wiki

Knowl uses lexical search by default. It matches indexed page text, titles,
headings and tags. With embeddings enabled, hybrid search combines lexical and
semantic candidates and returns evidence from the same published wiki. An
embedding model helps match related wording; it does not generate answers or
maintain pages.

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
| Semantic input | NFC/original case; 384 runes per chunk, 64 overlap; full page text within projection bounds, four chunks/query or source signals |
| Projection | At most 8,192 chunks, 1 MiB of page coverage metadata and 64 MiB total per scope; 15-minute rebuild or earlier caller deadline |

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

A rebuild replaces lexical state first, then generates full-page vectors in
batches of at most 16 without holding SQL locks. Complete dense publication
checks the canonical snapshot and every page's expected chunk count again;
concurrent change discards the stale build. A classified failure saves degraded
state even under strict policy. With lexical fallback, startup may be ready
while semantic retrieval is degraded; `/readyz` alone is not a hybrid guarantee.
Read the retrieval status. Invalid input and caller cancellation never return
successful fallback.

Queries never trigger a rebuild or download a model. After repairing a degraded
service, restart Knowl to retry the projection once during startup. Embedded
applications can call `knowl.RebuildProjection(ctx, config, snapshot)` explicitly.
Changing model/revision/dimensions/prefixes requires a complete projection rebuild
and changes maintenance identity. A strict projection error can occur after a
canonical commit: repair the derived projection, preserving the committed
facts and raw evidence.

Migration 16 adds disposable vector state and bounded operation reports in both
stores. Migration 21 expands the ordinal range and adds a bounded page coverage
manifest. Existing partial indexes are incompatible with the new preprocessing
identity and are rebuilt on startup. Down removes derived vector additions while
preserving canonical/raw content and older durable state. Stop writers and pair
the schema with a compatible binary before rollback. Disabling embeddings retains
the default lexical behavior and
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
