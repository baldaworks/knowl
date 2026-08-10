# Knowl

Knowl is an independent, local-first Markdown knowledge wiki. It turns bounded,
curated source revisions into a human-readable, cited wiki while keeping the
filesystem workspace as the source of truth.

In practice, Knowl has two jobs:

- accept immutable source revisions (`ingest`) and turn them into staged or
  committed wiki updates;
- answer reads (`query`, `search`, `page`, `page links`, `lint`) from the
  canonical workspace and its rebuildable SQL projections.

The canonical content lives in `knowledge/wiki/**`. SQL storage only keeps
durable operation state and rebuildable read projections. Knowl is local-first:
it binds only to loopback, expects a local workspace, and does not assume a
shared remote service.

## How it works

The normal flow is:

1. `knowl init` creates a workspace and a default local config.
2. `knowl validate` checks config, workspace shape, and selected runtime
   provider wiring.
3. `knowl ingest` or `knowl ingest preview` accepts one immutable source
   revision as JSON.
4. Knowl stores the raw source, asks the configured maintainer/runtime to plan
   wiki edits, and either:
   - stages the operation for review; or
   - applies it immediately, depending on `knowl.maintenance`.
5. Read commands work against the canonical wiki and its projections:
   - `knowl query`
   - `knowl search`
   - `knowl page`
   - `knowl page links`
   - `knowl lint`

The direct CLI commands run this workflow in-process and print structured JSON
to stdout. `knowl start` exposes the same host as a retained loopback HTTP API
for external local clients and OpenAPI-based tooling.

## Quick start

Build the CLI:

```bash
go build -o knowl ./cmd/knowl
```

Initialize a local workspace and config:

```bash
./knowl init
./knowl validate
```

`init` creates:

- a workspace at `./knowledge` by default;
- a config file at `.config/knowl/config.yaml`;
- a default SQLite operational store under `knowledge/.knowl/knowl.sqlite`.

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
    path: ./knowledge
  storage:
    type: sqlite
    sqlite:
      path: .knowl/knowl.sqlite
  maintenance:
    review: true
```

`knowl.provider` selects an entry from `runtime.providers`. Knowl's own config
is under the `knowl:` block.

Create one source revision JSON. The request shape is `SourceEnvelope`. Because
the `content` field is `[]byte`, JSON uses base64:

```json
{
  "scope": "local",
  "source": {"adapter": "fixture", "id": "source-1"},
  "version": {
    "version": "1",
    "digest": "6f1d2c6282e03687c92530aab89b6c13a06b5b8989267dfb048eec821e1af53f"
  },
  "content": "IyBPbmUKClRoaXMgaXMgYSBmaXh0dXJlIHNvdXJjZS4K"
}
```

With the default review-first policy, run the local workflow like this:

```bash
./knowl ingest preview --input source.json
./knowl operation <operation-id>
./knowl ingest apply <operation-id>
./knowl query "One"
./knowl search "One"
./knowl lint
```

If you disable review with `maintenance.review: false` or set
`maintenance.auto_apply: true`, normal ingest can apply directly:

```bash
./knowl ingest --input source.json
```

After apply, inspect generated content under `knowledge/wiki/**` and raw source
history under `knowledge/raw/**`.

## CLI usage model

The supported direct commands are:

- `knowl ingest --input FILE|-`
- `knowl ingest preview --input FILE|-`
- `knowl ingest apply <operation-id>`
- `knowl query <text>`
- `knowl query file --input FILE|-`
- `knowl search <text>`
- `knowl lint`
- `knowl operation <operation-id>`
- `knowl page <page-id>`
- `knowl page links <page-id>`

Complex write inputs use `--input FILE|-`. Read commands take positional
arguments and print structured JSON results to stdout. Non-2xx workflow errors
also return structured JSON, but the command exits non-zero.

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
workflows do not take per-request bearer headers because they execute as
trusted local in-process workflows.

## Repository layout

The canonical workspace layout is:

```text
knowledge/
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
