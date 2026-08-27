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

- `runtime:` — shared provider registry in Balda-compatible typed shape
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

Two-source example with optional automatic startup sync disabled:

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
    path: /var/lib/knowl/knowledge
  sources:
    - id: engineering
      type: filesystem
      filesystem:
        root: /sources/engineering
        include: ["**/*.md"]
        flavor: obsidian
      sync:
        on_start: false
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
        on_start: false
        interval: 5m
        retry_initial: 1s
        retry_maximum: 1m
    - id: catalog
      type: filesystem
      filesystem:
        root: /sources/catalog
        include: ["**/*"]
        flavor: okf
      sync:
        on_start: false
        interval: 5m
```

Notes:

- `knowl.provider` selects one entry from `runtime.providers`. A runnable host
  fails before readiness when no configured or explicitly injected maintainer
  is available.
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
# or: ./knowl bootstrap obsidian /path/to/vault
# or: ./knowl bootstrap okf /path/to/okf-bundle
```

Bootstrap retains its fresh-workspace guard but now performs exactly one shared
source sync using ID `bootstrap-wiki`, `bootstrap-obsidian`, or `bootstrap-okf`.
The `okf` flavor validates and preserves OKF v0.2 metadata, reserved controls,
Unicode paths, and standard concept links. A missing version is treated as v0.2;
another declared version is consumed best-effort and reported in the sync
result under `diagnostics`. A newly generated config includes a provider and
retains that source for later operation. If an operator-owned config already
exists, bootstrap does not rewrite it; add the source entry there before using
ongoing source commands. Ordinary source sync accepts existing workspaces,
stores source revisions only in `raw/`, and queues durable maintainer
operations. It never copies source content into `wiki/`. Bootstrap is optional;
an operator may initialize an empty workspace and sync configured sources later.

To convert a legacy canonical workspace, stop active writers, back it up, and
run `./knowl migrate okf-v0.2`. Migration is explicit and idempotent; startup,
`retrieve`, and `source status` never perform it. Validate and inspect retrieval
afterward before retiring a backup. See [workspace semantics](workspace.md) for
the recovery and archive contract.

The operational-store migration that adds generic hierarchy operations is
additive in both SQLite and PostgreSQL and leaves existing source operation IDs,
descriptors, leases, and statuses unchanged. Downgrading to an older Knowl
binary is safe only before any hierarchy operation row exists. After the first
reconcile, restore the pre-upgrade operational database for a binary rollback;
the canonical Markdown workspace remains portable and can rebuild a fresh
projection.

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

To explicitly replace a valid flat root with source-independent semantic
catalogs, stop other writers and run:

```bash
./knowl hierarchy reconcile
./knowl validate
```

This is the only hierarchy-specific mutation command. It constructs the normal
provider, workspace, and selected store in process, claims exactly its reserved
hierarchy operation, and does not start HTTP, the general operation scheduler,
source jobs, or configured `on_start` synchronization. Output is structured
JSON. A changed result includes its generation and affected catalog/log files;
a converged replay returns `"changed":false` and leaves the canonical digest
unchanged.

Planner identity `hierarchy-v3` makes this subject-first contract a new durable
operation identity. The maintainer is called once with deterministically ordered,
bounded page metadata, excerpts, current memberships, and the schema digest;
schema content, raw source bodies, provenance, and source-native paths are not
taxonomy input. It treats type and technology as supporting signals, recursively
decomposes broad heterogeneous subjects, permits sparse secondary membership for
cross-cutting pages, and tries to reuse suitable current semantic structure.
Semantic quality remains provider-dependent. Only this explicit command can
apply the result; startup and source synchronization do not reconcile catalogs.

Generated hierarchy controls are restricted to `wiki/index.md` and
`wiki/catalogs/**/index.md`. Ordinary concepts and `raw/` evidence are preserved
byte-for-byte. Planning is all-or-nothing and bounded: 1,024 pages, 1,024
catalogs, 16,384 edges, depth 16, 4 MiB input, 4,096 excerpt characters per
page, 1 MiB plan output, 1,024 edits, 256 KiB per catalog, and a 1 MiB manifest.
The command fails closed on a stale snapshot, invalid/incomplete graph, unsafe
path, an empty generated non-root catalog, or exceeded value and returns the
wrapped cause. An empty root is valid only for an empty wiki. See
[workspace semantics](workspace.md#explicit-semantic-hierarchy-reconciliation)
for ownership and recovery details.

These commands are operator conveniences, not the primary agent integration
surface. Source management is not exposed as an MCP tool.

`on_start` attempts are asynchronous. Each enabled source has independent
interval and capped retry state; a source cannot overlap itself, while different
sources can progress independently. A failed source leaves readiness and last
successful retrieval snapshots available. Inspect `source status` for durable
last-attempt/last-success state. After restart, recovery converges staged source
work before readiness.

A successful sync reports raw acceptance and maintenance reservation, not LLM
completion. `source status` reports bounded maintenance counts and samples for
queued, replayed, committed, and failed operations. Each sample correlates the
source document/revision with its operation ID and stable failure class without
including source bodies, prompts, or credentials.

The maintainer builds one root-reachable semantic OKF wiki. Related evidence
from different sources may support the same page; retrieve returns all resolved
`source_documents`, and a source filter matches when any supporting document
belongs to that configured source. Legacy derived
`wiki/sources/<source_id>/**` trees are removed on that source's next successful
reconciliation without deleting raw history or curated pages.

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
