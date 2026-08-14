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
  knowl:local
```

Or use the checked-in Compose example:

```bash
docker compose -f deploy/sidecar/compose.yaml up --build
```

Set `KNOWL_IMAGE` to use a prebuilt image with Compose. Production deployments
should pin an immutable manifest digest instead of a mutable tag.

The first published distribution is documented in the
[v0.1.0 release notes](releases/v0.1.0.md), including authenticated run,
upgrade, and non-destructive rollback guidance.

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
