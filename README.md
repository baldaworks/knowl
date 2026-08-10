# Knowl

Knowl is an independent, local-first Markdown knowledge wiki. It turns bounded,
curated source revisions into a human-readable, cited wiki while keeping the
filesystem workspace as the source of truth.

## Supported local workflow

The supported local operator path is:

```bash
go build -o knowl ./cmd/knowl
./knowl init
./knowl validate
./knowl start
curl -sS http://127.0.0.1:8080/readyz
```

`start` listens on `127.0.0.1:8080` by default. `readyz` returns a JSON
response with `status: "ready"` and `scope: "local"` after workspace
validation, SQL setup, recovery, and projection preparation succeed.

Configuration is loaded from `.config/knowl/config.yaml`, then the selected
profile and `KNOWL_*` overrides. Use `--config-dir` or `--profile` to select
another config source.

The supported ingest, query, lint, and apply workflows go through the loopback
HTTP API after `knowl start`. The `ingest` and `lint` Cobra command names
remain visible for compatibility, but they are not the supported local
workflow today. See [local operations](docs/operations.md) for the verified
config shape, operator-token auth, and concrete HTTP examples.

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

## Workspace

The canonical layout is immutable raw source versions plus governed Markdown:

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
    └── knowl.sqlite       # SQLite operational state, when selected
```

`schema.md`, raw sources, page frontmatter, links, citations, recovery, and Git
guidance are described in [workspace semantics](docs/workspace.md). SQL stores
operations and rebuildable projections only; they never replace the Markdown
artifact.

## Development

Use these commands as the maintainer verification story for the supported
workflow:

```bash
# Proves the supported local workflow claim on current HEAD:
# - cmd/knowl smoke coverage runs the real init -> validate -> host startup path
# - pkg/knowl covers the plain-Go embedding entrypoint
# - pkg/knowlfx covers the Fx embedding entrypoint
GOCACHE=/tmp/knowl-test-cache go test ./...

# Optional broader checks for maintainers:
GOCACHE=/tmp/knowl-race-cache go test -race ./...
go vet ./...
go build ./...

# Optional PostgreSQL/Testcontainers coverage. Requires Docker/Podman and stays
# outside the default suite.
go test -tags=integration ./pkg/knowl/store/postgres -run TestStoreContractWithTestcontainers -count=1
```

The repository uses `go.uber.org/fx` in `pkg/knowlfx` and `modernc.org/sqlite`
for the SQLite adapter. Balda integration is deliberately adapter-only; Knowl
does not import Balda storage, sessions, transports, or memory policy.
