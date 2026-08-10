# Local operations

This document is the detailed local reference for the current supported Knowl
workflow. `README.md` is the task-first entrypoint; this document expands the
verified config shape, lifecycle, HTTP surface, auth rules, storage behavior,
and public embedding choices.

## Supported local workflow

Run the local operator path in this order:

```bash
go build -o knowl ./cmd/knowl
./knowl init
./knowl validate
./knowl start
curl -sS http://127.0.0.1:8080/readyz
```

The direct CLI covers workspace bootstrap plus read commands such as `query`,
`search`, `page`, and `lint`. After `knowl start`, low-level ingest, review,
apply, and integration workflows go through the loopback HTTP/OpenAPI API.

## Configuration

The CLI reads `.config/knowl/config.yaml` by default. `--config-dir` selects an
additional config root and `--profile` selects a top-level profile using the
same loader semantics as Balda. The document has two sections: the shared
Norma runtime registry and the Knowl application settings. A complete SQLite
example is:

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
  maintenance:
    review: true
    # auto_apply: false
```

For PostgreSQL, replace only the typed storage selection with:

```yaml
  storage:
    type: postgres
    postgres:
      dsn: ${KNOWL_POSTGRES_DSN}
```

`knowl.provider` is only a selector; it must name an entry in
`runtime.providers`. Runtime entries retain Balda's discriminated shape:
`type` plus the matching type-specific block. Knowl storage follows the same
pattern: `knowl.storage.type` selects one backend, and only the matching
optional block (`sqlite` or `postgres`) may be present. The CLI defaults to
SQLite when the storage section is omitted, and `knowl init` writes the
explicit SQLite block. A selected PostgreSQL block requires a non-empty DSN.

The loader applies `KNOWL_*` overrides to leaf keys present in the loaded
document; common application keys include `KNOWL_WORKSPACE_PATH`,
`KNOWL_PROVIDER`, `KNOWL_STORAGE_TYPE`, `KNOWL_STORAGE_SQLITE_PATH`,
`KNOWL_STORAGE_POSTGRES_DSN`, `KNOWL_SERVER_LISTEN_ADDR`,
`KNOWL_OPERATOR_TOKEN`, `KNOWL_MAINTENANCE_REVIEW`, and
`KNOWL_MAINTENANCE_AUTO_APPLY`. Provider credentials should be supplied through
the shared runtime configuration's normal environment expansion, not printed
in diagnostics.

The listener must be loopback (`localhost`, `127.0.0.1`, or another loopback
IP); remote/shared deployment is outside this local contract. A relative
SQLite path is resolved below the workspace.

`maintenance.review: true` is the conservative default. Setting
`maintenance.auto_apply: true` (or `review: false`) permits normal ingest to
apply after validation. Preview always forces `awaiting_review`, even when
auto-apply is configured. Writes also require a configured operator token and a
matching `Authorization: Bearer ...` header.

Startup validates the selected runtime provider before opening the workspace,
SQL store, worker, or listener. Provider execution remains lazy: `validate` and
`start` do not contact the model, while ingest planning invokes the selected
provider through Knowl's structured maintainer adapter. There is no silent
read-only `unavailableMaintainer` fallback.

## Start and lifecycle

```bash
./knowl init
./knowl validate
./knowl start
```

Host construction performs, in order, workspace validation, selected SQL
migration, filesystem recovery, a canonical snapshot, and projection readiness
(rebuild if needed). Only then does the HTTP host become ready. The Fx-owned
host lifecycle is:

1. `Start` binds the loopback listener, starts the bounded single-consumer
   writer, and marks readiness true. It does not block on the server loop.
2. The service waits for cancellation or a fatal HTTP server error.
3. `Stop` marks readiness false, stops accepting new work, drains or interrupts
   queued writes within the shutdown deadline, shuts down HTTP, recovers the
   workspace, closes the provider-backed maintainer, and closes the SQL store.

The same lifecycle is available publicly through `pkg/knowlfx.NewApp`.
Callers that do not want Fx can construct and manage a host directly through
root `pkg/knowl`. Core policy does not depend on Fx; callers using
`pkg/knowl/app` can also compose their own lifecycle around the lower-level
services.

## Health, readiness, and auth

Read-only health endpoints do not require the operator token:

```bash
curl -sS http://127.0.0.1:8080/healthz
curl -sS http://127.0.0.1:8080/readyz
```

- `/healthz` returns HTTP 200 with JSON containing `service: "knowl"` and
  `status: "ok"`.
- `/readyz` returns HTTP 200 with JSON containing `service: "knowl"`,
  `status: "ready"`, and the trusted `scope` after recovery, migration, and
  projection preparation complete.

Authenticated write endpoints require the operator token configured under
`knowl.operator.token`:

- `POST /v1/ingest`
- `POST /v1/ingest/preview`
- `POST /v1/operations/{id}/apply`
- `POST /v1/query/file`

Provide it as `Authorization: Bearer ...`. Missing or mismatched credentials
return `operator_authorization_required`.

## Ingest, review, and filing

The trusted loopback HTTP API exposes:

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/ingest` | Accept, plan, and apply or review one source revision according to policy |
| `POST` | `/v1/ingest/preview` | Accept and stage one source revision, always awaiting review |
| `POST` | `/v1/operations/{id}/apply` | Apply a staged reviewed operation |
| `GET` | `/v1/operations/{id}` | Read redacted operation status |
| `GET` | `/v1/operations/{id}/status` | Status alias |
| `GET` | `/v1/status/{id}` | Status alias |
| `POST` | `/v1/query/file` | Explicitly file a query result and typed plan through the same gate |

The source envelope is bounded textual input. In JSON, Go `[]byte` content is
base64 encoded:

```json
{
  "scope": "local",
  "source": {"adapter": "fixture", "id": "source-1"},
  "version": {
    "version": "1",
    "digest": "a28130a037ced440a70f1e1e896d9f6cf7147adc31e233e588f58dde12902a2d"
  },
  "content": "c291cmNlIHRleHQ="
}
```

### Authenticated preview and apply example

The runtime tests verify this preview flow:

```bash
export KNOWL_TOKEN=replace-with-a-local-secret

curl -sS \
  -H "Authorization: Bearer ${KNOWL_TOKEN}" \
  -H "Content-Type: application/json" \
  http://127.0.0.1:8080/v1/ingest/preview \
  -d '{
    "scope": "local",
    "source": {"adapter": "fixture", "id": "source-1"},
    "version": {
      "version": "1",
      "digest": "a28130a037ced440a70f1e1e896d9f6cf7147adc31e233e588f58dde12902a2d"
    },
    "content": "c291cmNlIHRleHQ="
  }'
```

The response includes an `operation` whose status is `awaiting_review`. Copy
that operation ID into the apply step:

```bash
curl -sS \
  -X POST \
  -H "Authorization: Bearer ${KNOWL_TOKEN}" \
  http://127.0.0.1:8080/v1/operations/<operation-id>/apply
```

The apply response returns the same operation in `committed` state. Preview is
always review-first, even when `maintenance.auto_apply` is enabled.

The host supplies its trusted scope when the envelope omits one and rejects a
different scope. It rejects query-string scope overrides. The operation key is
`(scope, adapter, source ID, version)`; a digest conflict never overwrites an
accepted raw source.

The normal state progression is:

```text
received -> planned -> awaiting_review -> applying -> committed
planned  -> applying -> committed
planned  -> failed
```

With explicit auto-apply, `app.IngestService` advances from `planned` to
`applying`; otherwise it stages the plan and returns `awaiting_review`. Preview
uses the latter path unconditionally. Apply validates the retained staging
manifest, schema digest, prior file digests, and recovery journal before making
the Markdown generation visible. Replaying a committed source revision returns
the retained operation rather than duplicating edits.

The query filing request contains `query`, the bounded `result`, and a typed
`plan`. Filing creates an immutable query-result source and sends it through the
same source acceptance, schema, review, plan, staging, commit, and projection
policy. Read-only query never files implicitly.

## Reads, search, and lint

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/healthz` or `/health` | Liveness |
| `GET` | `/readyz` or `/health/ready` | Recovery/migration/projection readiness |
| `GET` | `/v1/search?q=...` | Bounded page references |
| `GET` | `/v1/query?q=...` | Bounded pages, links, and wiki/raw citations |
| `GET` | `/v1/pages/{page-id}` | One bounded page read |
| `GET` | `/v1/pages/{page-id}/links` | One bounded link neighborhood |
| `GET` | `/v1/lint`, `/v1/lint/results`, or `/v1/lint-results` | Deterministic health report |

After the preview/apply example commits the page, these read-only requests do
not require the operator token:

```bash
curl -sS http://127.0.0.1:8080/v1/operations/<operation-id>
curl -sS http://127.0.0.1:8080/v1/pages/entities/one
curl -sS http://127.0.0.1:8080/v1/lint
```

Read results carry provenance and are marked untrusted where appropriate.
Default read limits are 20 pages, 4 MiB, 32 KiB of characters, depth 8, and a
30-second deadline; callers cannot expand them through the HTTP query surface.
Lint checks malformed frontmatter, missing/unknown citations, duplicate IDs,
malformed/broken links, missing/orphan/stale index entries, log consistency,
raw-source integrity, and SQL projection drift. Optional maintainer lint is
suggestion-only and cannot mutate or fetch external sources.

## Public embedding surface

Choose the public package by responsibility:

- `pkg/knowl/types` for transport-neutral domain contracts shared across
  transports or callers that do not need to construct a host.
- root `pkg/knowl` for the plain-Go host composition API. See
  `pkg/knowl/example_test.go` for the verified `NewHost` example.
- `pkg/knowlfx` for an Fx-managed `fx.App` wrapper over the same host. See
  `pkg/knowlfx/example_test.go` for the verified `NewApp` example.

Root `pkg/knowl` still exports transition aliases for the domain contracts, but
new code should prefer `pkg/knowl/types` directly when it only needs the shared
types. The canonical lower-level application policy remains in `pkg/knowl/app`;
most embedders should start with root `pkg/knowl` or `pkg/knowlfx`.

## MCP tools

`pkg/knowl/mcp` exposes a transport-neutral registry with the following five
read-only tools:

- `search(query)`
- `read-page(id)`
- `links(id)`
- `operation-status(id)`
- `lint-results()`

The server stores one trusted scope and bounded read limits. Tool arguments may
not include `scope`; ingest, apply, query filing, schema modification, and
delete/forget tools are absent. A transport adapter should expose `Tools` or
`ListTools` and dispatch through `Call`/`CallTool`; the registry itself does not
grant filesystem or SQL access beyond the application ports.

## Stores, migrations, and rebuilds

Both stores implement the same `app.OperationStore` and `app.SearchIndex` ports.
Each embeds a dialect-specific Goose migration and runs it during `Open`:

- SQLite uses `modernc.org/sqlite`, one connection, WAL where available, and a
  busy timeout. Its default file is `.knowl/knowl.sqlite`.
- PostgreSQL uses `pgx`, a bounded connection pool, PostgreSQL full-text search,
  and the configured DSN. The DSN is never returned in redacted diagnostics.

The positive PostgreSQL adapter contract can run against an isolated
Testcontainers instance without requiring a checked-in DSN:

```bash
go test -tags=integration ./pkg/knowl/store/postgres -run TestStoreContractWithTestcontainers -count=1
```

This integration command requires a Docker- or Podman-compatible container
runtime. Ordinary `go test ./...` does not start containers.

The SQL schema contains operation state, leases, page/link projections, and
projection metadata. A canonical `WorkspaceSnapshot` can rebuild the
projection through `SearchIndex.Rebuild`; startup performs this when the stored
schema/snapshot digest is not ready, and lint reports later drift. There is no
remote migration or automatic publication command. Stop the host before a
filesystem/SQLite backup; use the normal PostgreSQL backup tooling for a
PostgreSQL deployment.

## Diagnostics and safety

HTTP errors expose stable classes such as `not_found`, `invalid_request`,
`service_unavailable`, `operation_not_applyable`, and `operation_failed` rather
than source bodies, page bodies, provider output, credentials, or DSNs.
Scope is host configuration, not user-controlled request data. Raw sources,
Markdown, retrieved pages, and provider plans are untrusted content and cannot
change paths, scope, validation, or available tools.
