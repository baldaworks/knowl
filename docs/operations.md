# Service operations

This document is the operator-facing reference for running Knowl as a service.

If you only need the product overview, start with [README.md](../README.md).
If you need the baseline container path, see [sidecar deployment](sidecar.md).

## Runtime model

Knowl is a standalone knowledge service with:

- a canonical workspace;
- one ingest pipeline;
- rebuildable operational state and projections;
- MCP and HTTP transports over the same application services.

Baseline deployment is service/sidecar mode. Fx embedding is the alternative
for Go applications that want the same runtime in-process.

## Configuration

The CLI loads `.config/knowl/config.yaml` by default. `--config-dir` selects an
additional config root and `--profile` selects a top-level profile.

The config has two sections:

- `runtime:` — optional shared provider registry in Balda-compatible typed shape
- `knowl:` — Knowl application settings

SQLite example:

```yaml
runtime:
  providers:
    opencode:
      type: opencode_acp
      opencode_acp:
        model: opencode/big-pickle

knowl:
  provider: opencode
  workspace:
    path: .
  storage:
    type: sqlite
    sqlite:
      path: .knowl/knowl.sqlite
  scope: local
  server:
    listen_addr: 127.0.0.1:8080
  operator:
    token: replace-with-a-local-secret
```

PostgreSQL example:

```yaml
knowl:
  storage:
    type: postgres
    postgres:
      dsn: ${KNOWL_POSTGRES_DSN}
```

Container baseline example:

```yaml
knowl:
  workspace:
    path: /var/lib/knowl/knowledge
  storage:
    type: sqlite
    sqlite:
      path: .knowl/knowl.sqlite
  server:
    listen_addr: 0.0.0.0:8080
```

Provider-free two-source example:

```yaml
knowl:
  provider: ""
  workspace:
    path: /var/lib/knowl/knowledge
  sources:
    - id: engineering
      type: filesystem
      filesystem:
        root: /sources/engineering
        include: ["**/*.md"]
        flavor: obsidian
      sync:
        on_start: true
        interval: 5m
        retry_initial: 1s
        retry_maximum: 1m
    - id: operations
      type: filesystem
      filesystem:
        root: /sources/operations
        include: ["**/*.md"]
        flavor: markdown
      sync:
        on_start: true
        interval: 5m
        retry_initial: 1s
        retry_maximum: 1m
```

Notes:

- `knowl.provider` optionally selects one entry from `runtime.providers`.
  Without it, retrieval, lint, health, and configured source workflows remain
  available; ingest returns HTTP 503 with `maintainer_unavailable` and performs
  no source acceptance or durable operation reservation.
- `knowl.storage.type` selects one optional typed storage block.
- when storage is omitted, Knowl defaults to SQLite.
- default local listen address is `127.0.0.1:8080`; service/sidecar deployments
  may override it with `0.0.0.0:8080` or another literal IP bind.
- when `knowl.operator.token` is non-empty, `/v1/*` and `/mcp` require an
  `Authorization: Bearer <token>` header. Health and readiness probes remain
  unauthenticated. Keep tokenless deployments on a trusted, loopback-only
  network boundary.

Common `KNOWL_*` overrides include:

- `KNOWL_PROVIDER`
- `KNOWL_WORKSPACE_PATH`
- `KNOWL_STORAGE_TYPE`
- `KNOWL_STORAGE_SQLITE_PATH`
- `KNOWL_STORAGE_POSTGRES_DSN`
- `KNOWL_SERVER_LISTEN_ADDR`
- `KNOWL_OPERATOR_TOKEN`

## Supported operator workflow

Local workspace bootstrap:

```bash
go build -o knowl ./cmd/knowl
./knowl bootstrap wiki /path/to/wiki
```

Bootstrap retains its fresh-workspace guard but now performs exactly one shared
source sync using ID `bootstrap-wiki` or `bootstrap-obsidian`. A newly generated
config is provider-free and retains that source for later operation. If an
operator-owned config already exists, bootstrap does not rewrite it; add the
source entry there before using ongoing source commands. Ordinary source sync
accepts existing workspaces and preserves curated pages and `wiki/index.md`.
Its only canonical target is `wiki/sources/<source_id>/**`.

Empty workspace initialization:

```bash
./knowl init
./knowl validate
./knowl start
```

Sidecar baseline:

```bash
docker compose -f deploy/sidecar/compose.yaml up --build
```

The CLI commands `retrieve`, `ingest`, and `operation` are one-shot operator
wrappers over the same service semantics. Source controls run directly against
an in-process Host without starting HTTP or scheduled runners:

```bash
./knowl source list
./knowl source sync engineering
./knowl source sync --all
./knowl source status engineering
```

These commands are operator conveniences, not the primary agent integration
surface. Source management is not exposed as an MCP tool.

`on_start` attempts are asynchronous. Each enabled source has independent
interval and capped retry state; a source cannot overlap itself, while different
sources can progress independently. A failed source leaves readiness and last
successful retrieval snapshots available. Inspect `source status` for durable
last-attempt/last-success state. After restart, recovery converges staged source
work before readiness.

## HTTP contract

Authoritative contract: [api/openapi/knowl.yaml](../api/openapi/knowl.yaml)

Business endpoints:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/v1/retrieve?query=...` | Retrieve bounded evidence with provenance |
| `POST` | `/v1/ingest` | Submit one text or URI source |
| `GET` | `/v1/operations/{operation_id}` | Read one durable public operation status |

Operational endpoints:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/healthz` | Process liveness |
| `GET` | `/readyz` | Workspace/store/projection readiness |

The public state model is:

```text
queued -> running -> completed | failed
```

The trusted scope is owned by the host or service configuration. Callers must
not supply a different scope through public request arguments.

## HTTP examples

Readiness:

```bash
curl -sS http://127.0.0.1:8080/readyz
```

Retrieve:

```bash
curl -sS \
  -H "Authorization: Bearer $KNOWL_OPERATOR_TOKEN" \
  "http://127.0.0.1:8080/v1/retrieve?query=Why%20was%20Badger%20chosen%3F"
```

Ingest text:

```bash
curl -sS \
  -H "Authorization: Bearer $KNOWL_OPERATOR_TOKEN" \
  -H "Content-Type: application/json" \
  http://127.0.0.1:8080/v1/ingest \
  -d '{
    "content": "Badger was chosen because ...",
    "origin": "ticket-1234",
    "idempotency_key": "ticket-1234"
  }'
```

Ingest URI:

```bash
curl -sS \
  -H "Authorization: Bearer $KNOWL_OPERATOR_TOKEN" \
  -H "Content-Type: application/json" \
  http://127.0.0.1:8080/v1/ingest \
  -d '{
    "uri": "https://example.com/adr/session-memory-store"
  }'
```

Poll operation:

```bash
curl -sS \
  -H "Authorization: Bearer $KNOWL_OPERATOR_TOKEN" \
  http://127.0.0.1:8080/v1/operations/op_01K...
```

## MCP contract

MCP is the primary agent-facing interface.

The running service exposes MCP Streamable HTTP on its existing listener at
`http://127.0.0.1:8080/mcp`.

When an operator token is configured, MCP clients must send the same bearer
token in the HTTP `Authorization` header.

The baseline server exposes exactly:

- `knowl_retrieve`
- `knowl_ingest`
- `knowl_operation`

MCP and HTTP call the same underlying application services.

## Lifecycle and readiness

Host construction performs:

1. workspace validation;
2. selected-store setup/migration;
3. recovery;
4. projection preparation;
5. listener startup.

`/healthz` only means the process is serving HTTP.

`/readyz` means:

- workspace is usable;
- store is open;
- recovery completed;
- projections are ready for retrieve/operation reads.

## Sidecar notes

The checked-in sidecar assets assume:

- Knowl owns `/var/lib/knowl`;
- the canonical workspace is `/var/lib/knowl/knowledge`;
- the agent talks to Knowl over MCP or the same KISS HTTP contract;
- the agent does not mutate `raw/`, `wiki/`, or `.knowl/` directly.

## Fx embedding

For Go applications:

- root `pkg/knowl` is the non-Fx runtime entrypoint;
- `pkg/knowlfx.NewApp` wraps the same runtime with Fx lifecycle management.

This is an alternative deployment/composition mode, not a second product API.
