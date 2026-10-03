# Sidecar deployment

Knowl's baseline deployment shape is a sidecar service with its own persistent
storage.

The repository ships:

- [Dockerfile](../Dockerfile) to build the `knowl` service image;
- [deploy/sidecar/knowl.yaml](../deploy/sidecar/knowl.yaml) as the baseline
  container config;
- [deploy/sidecar/compose.yaml](../deploy/sidecar/compose.yaml) as the minimal
  local sidecar example.

## What the container does

On startup the container runs:

1. `knowl --config-dir /etc init`
2. `knowl --config-dir /etc start`

That gives an empty persistent volume a valid Knowl workspace on first start,
then launches the service on `0.0.0.0:8080`.

The container owns:

- `/var/lib/knowl/knowledge` as the canonical workspace;
- `/var/lib/knowl/knowledge/.knowl/knowl.sqlite` as the default SQLite
  operational store.

Mount a persistent volume at `/var/lib/knowl`. Do not mount the agent directly
into Knowl's workspace and do not let the agent mutate `wiki/**` itself.
Mount authoritative wiki roots separately and read-only. The checked-in Compose
example mounts `/sources/engineering` and `/sources/operations`. The source
reconciler stores their exact accepted revisions under `raw/`; the maintainer
builds the separate semantic OKF artifact under `wiki/`.

## Build and run

Build the image:

```bash
docker build \
  --build-arg VERSION=local \
  --build-arg REVISION="$(git rev-parse HEAD)" \
  --build-arg CREATED="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -t knowl:local .
```

Release images run as a non-root user and carry OCI source, revision, version,
license, and creation metadata. For a local release-shaped persistence and
authentication check, run `scripts/smoke-test-sidecar.sh knowl:local`.

Run it directly:

```bash
docker run --rm \
  -p 127.0.0.1:8080:8080 \
  -v knowl-data:/var/lib/knowl \
  -v /path/to/engineering:/sources/engineering:ro \
  -v /path/to/operations:/sources/operations:ro \
  knowl:local
```

Or use the checked-in Compose example:

```bash
docker compose -f deploy/sidecar/compose.yaml up --build
```

Set `KNOWL_IMAGE` to use a prebuilt image with Compose. Production deployments
should pin an immutable manifest digest instead of a mutable tag.

The multi-source rollout is documented in the
[v0.2.0 release notes](releases/v0.2.0.md). The first published distribution and
its non-destructive rollback guidance remain in the
[v0.1.0 release notes](releases/v0.1.0.md).

The active baseline config includes `runtime.providers` plus `knowl.provider`.
A runnable sidecar requires a maintainer and fails before readiness when the
selection is absent or invalid. Initial bootstrap is optional. Each source's
`sync.on_start` flag is also explicit configuration rather than an implicit
bootstrap requirement.

Each source syncs on start and periodically with bounded retry. A failed source
does not make the service unready or discard the last successful snapshots.
Persist the whole `/var/lib/knowl` volume so source status, tombstones, raw
history, recovery journals, and SQLite state survive restart.

Sync accepts raw revisions and reserves durable maintenance work; model-backed
wiki changes complete asynchronously. Inspect bounded maintenance counts and
operation-correlated samples with `knowl source status <source-id>`.

## Optional CPU embeddings

From this checkout, opt in with the separately checked-in
[embedding config](../deploy/sidecar/embeddings.yaml) and
[Compose overlay](../deploy/sidecar/embeddings.compose.yaml):

```bash
docker compose -f deploy/sidecar/compose.yaml \
  -f deploy/sidecar/embeddings.compose.yaml up --build -d
```

The baseline/published quickstart remains lexical-only. Build the current
checkout or use an image containing this configuration contract. The overlay
uses the same maintainer/source setup as the baseline; embeddings do not replace
maintainer credentials or source configuration.

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
stack with `down` preserves its named volumes. The
[operations guide](operations.md#optional-embeddings) covers external APIs,
credentials, budgets, strict failures and projection recovery.

## Health checks

- `GET /healthz` means the process is serving HTTP.
- `GET /readyz` means workspace recovery, store setup, and projections are
  ready.

For local verification:

```bash
curl -sS http://127.0.0.1:8080/healthz
curl -sS http://127.0.0.1:8080/readyz
```

## Agent-side use

The agent or host app runs next to Knowl and talks to it over MCP or the same
KISS HTTP contract:

- MCP Streamable HTTP: `/mcp`
- `GET /v1/retrieve`
- `POST /v1/ingest`
- `GET /v1/operations/{operation_id}`

Keep the published port loopback-only for local sidecar use.

## Connect an MCP client

With the service running, configure a Streamable HTTP MCP client using the
service URL and operator token:

```json
{
  "transport": "streamable_http",
  "url": "http://127.0.0.1:8080/mcp",
  "headers": {
    "Authorization": "Bearer <operator-token>"
  }
}
```

Adapt the configuration shape to your client. The three tools are
`knowl_retrieve`, `knowl_ingest`, and `knowl_operation`. Their equivalent HTTP
endpoints are `GET /v1/retrieve`, `POST /v1/ingest`, and
`GET /v1/operations/{operation_id}`; request and response schemas are in the
[OpenAPI contract](../api/openapi/knowl.yaml).

For local Codex with host-owned MCP stdio, use the
[local Codex guide](local-codex.md).
