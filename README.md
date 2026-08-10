# Knowl

Knowl is an independent, local-first Markdown knowledge wiki. It turns bounded,
curated source revisions into a human-readable, cited wiki while keeping the
filesystem workspace as the source of truth.

In practice, Knowl has two user-facing modes:

- bootstrap an existing Markdown tree or Obsidian vault into a Knowl-owned
  local workspace;
- answer reads (`query`, `search`, `page`, `page links`, `lint`) from the
  canonical workspace and its rebuildable SQL projections.

The canonical content lives in `workspace/wiki/**`. SQL storage only keeps
durable operation state and rebuildable read projections. Knowl is local-first:
it binds only to loopback, expects a local workspace, and does not assume a
shared remote service. Low-level write/review workflows remain available
through the loopback HTTP/OpenAPI API rather than the direct CLI.

## How it works

The normal flow is:

1. Bootstrap an existing wiki or vault with `knowl bootstrap ...`, or create an
   empty workspace with `knowl init`.
2. `knowl validate` checks config, workspace shape, and selected runtime
   provider wiring.
3. Read commands work directly against the canonical wiki and its projections:
   - `knowl query`
   - `knowl search`
   - `knowl page`
   - `knowl page links`
   - `knowl lint`
4. `knowl start` exposes the retained loopback HTTP/OpenAPI host when you need
   low-level ingest/review/apply workflows or external local clients.

Direct CLI read commands run in-process and print structured JSON to stdout.

## Quick start

Build the CLI:

```bash
go build -o knowl ./cmd/knowl
```

Bootstrap an existing Markdown tree into a local Knowl workspace:

```bash
./knowl bootstrap wiki /path/to/existing/wiki
```

Or initialize an empty workspace and config:

```bash
./knowl init
./knowl validate
```

`init` creates:

- a workspace rooted at the current directory by default;
- a config file at `.config/knowl/config.yaml`;
- a default SQLite operational store under `.knowl/knowl.sqlite`.

A minimal config shape is:

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
  ingest:
    auto_apply: false
```

`knowl.provider` selects an entry from `runtime.providers`. Knowl's own config
is under the `knowl:` block. `knowl.ingest.auto_apply` controls normal ingest:
`false` keeps writes in review, `true` applies automatically after validation.
Preview remains review-first regardless of this setting.

Run local reads directly:

```bash
./knowl query "One"
./knowl search "One"
./knowl lint
```

Run the retained local host when you need HTTP/OpenAPI access:

```bash
./knowl start
curl -sS http://127.0.0.1:8080/readyz
```

Low-level ingest/review/apply flows are still supported, but only through the
loopback HTTP/OpenAPI API documented in [docs/operations.md](docs/operations.md).

## CLI usage model

The supported direct commands are:

- `knowl bootstrap wiki <path>`
- `knowl bootstrap obsidian <path>`
- `knowl init`
- `knowl validate`
- `knowl start`
- `knowl query <text>`
- `knowl search <text>`
- `knowl lint`
- `knowl page <page-id>`
- `knowl page links <page-id>`

Read commands take positional arguments and print structured JSON results to
stdout. Workspace lifecycle commands emit human-oriented logs to stderr. Write,
review, and apply workflows are exposed through the loopback HTTP/OpenAPI API,
not the direct CLI.

Configuration loads from `.config/knowl/config.yaml`, then the selected profile
and `KNOWL_*` overrides. Use `--config-dir` or `--profile` to select another
config source.

## Service mode

If you want a long-running local host instead of one-shot CLI execution:

```bash
./knowl start
curl -sS http://127.0.0.1:8080/readyz
```

`start` listens on `127.0.0.1:8080` by default. The loopback HTTP/OpenAPI API
is the retained transport for:

- local external clients;
- health/readiness checks;
- generated clients and OpenAPI tooling.

`readyz` returns JSON with `status: "ready"` and the trusted `scope` after
workspace validation, SQL setup, recovery, and projection preparation succeed.

Loopback HTTP writes still require the configured operator token. Direct CLI
read workflows do not take per-request bearer headers because they execute as
trusted local in-process workflows.

## Repository layout

Inside the configured workspace root, the canonical layout is:

```text
workspace/
├── schema.md
├── raw/
├── wiki/
│   ├── index.md
│   ├── log.md
│   ├── entities/
│   ├── concepts/
│   └── syntheses/
└── .knowl/
    ├── staging/
    ├── recovery/
    └── knowl.sqlite
```

- `schema.md` defines operator-owned wiki rules;
- `raw/` stores accepted immutable source revisions;
- `wiki/` stores canonical Markdown output;
- `.knowl/` stores staging, recovery, and local operational state.

## Where to look next

Use these docs by question:

- “How do I run it locally?” → [local operations](docs/operations.md)
- “What files does it own?” → [workspace semantics](docs/workspace.md)
- “How is it structured internally?” → [architecture](docs/architecture.md)
- “What are the integration boundaries?” → [integration boundaries](docs/integrations.md)
- “What is the public HTTP contract?” → [api/openapi/knowl.yaml](api/openapi/knowl.yaml)

The canonical public HTTP contract lives in [api/openapi/knowl.yaml](api/openapi/knowl.yaml).
The checked-in `oapi-codegen` config lives in
[api/openapi/oapi-codegen.yaml](api/openapi/oapi-codegen.yaml), and the
generated transport bindings live in
[internal/httpapi/knowlapi/server.gen.go](internal/httpapi/knowlapi/server.gen.go).
When the public HTTP contract changes, regenerate the bindings with:

```bash
go generate ./internal/httpapi/knowlapi
```

## Public packages

Use the public package split like this:

- `pkg/knowl/types` for transport-neutral domain contracts
- root `pkg/knowl` for the plain-Go host composition API
- `pkg/knowlfx` for the Fx-managed lifecycle wrapper over root `pkg/knowl`

Root `pkg/knowl` still exports transition aliases for the domain contracts,
but new embedding code should prefer `pkg/knowl/types` directly. Plain
embedders can construct and manage a host through root `pkg/knowl`; Fx
embedders can use `pkg/knowlfx.NewApp`.

For detailed config, endpoint, lifecycle, storage, and embedding examples, see
[local operations](docs/operations.md). For deeper repository internals, see
[workspace semantics](docs/workspace.md), [architecture](docs/architecture.md),
and [integration boundaries](docs/integrations.md).

## Development

Use these commands as the maintainer verification story for the supported
workflow:

```bash
# Regenerate checked-in server bindings after changing the spec or generator config.
go generate ./internal/httpapi/knowlapi

# Proves the supported local workflow claim on current HEAD:
# - internal/httpapi/knowlapi checks spec-to-generated route parity
# - cmd/knowl smoke coverage runs the real init -> validate -> host startup path
# - pkg/knowl covers the plain-Go embedding entrypoint and runtime HTTP behavior
# - pkg/knowlfx covers the Fx embedding entrypoint
GOCACHE=/tmp/knowl-test-cache go test ./...

# Optional broader checks for maintainers:
GOCACHE=/tmp/knowl-race-cache go test -race ./...
go vet ./...
go build ./...
go tool golangci-lint run ./...

# Optional PostgreSQL/Testcontainers coverage. Requires Docker/Podman and stays
# outside the default suite.
go test -tags=integration ./pkg/knowl/store/postgres -run TestStoreContractWithTestcontainers -count=1
```

The repository uses `go.uber.org/fx` in `pkg/knowlfx` and `modernc.org/sqlite`
for the SQLite adapter. Balda integration is deliberately adapter-only; Knowl
does not import Balda storage, sessions, transports, or memory policy.
