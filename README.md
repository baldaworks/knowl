# Knowl

Knowl is an independent, local-first Markdown knowledge wiki. It turns bounded,
curated source revisions into a human-readable, cited wiki while keeping the
filesystem workspace as the source of truth.

The public package split is explicit:

- `pkg/knowl/types` contains transport-neutral domain contracts
- `pkg/knowl/wiki` contains shared frontmatter and wiki-link semantics
- root `pkg/knowl` contains the plain-Go host composition API
- `pkg/knowlfx` contains the Fx lifecycle wrapper over root `pkg/knowl`
- `pkg/knowl/app` contains canonical application policy

The local `knowl` process composes the filesystem adapter, SQLite or
PostgreSQL operational state, a serialized writer, the loopback HTTP operator
API, and bounded read-only MCP tools through the public `pkg/knowl` +
`pkg/knowlfx` boundary.

## Quick start

```bash
go build -o knowl ./cmd/knowl
./knowl init
./knowl validate
./knowl start
```

`start` listens on `127.0.0.1:8080` by default. Configuration is loaded from
`.config/knowl/config.yaml`, then the shared profile and `KNOWL_*` overrides.
Use `--config-dir` or `--profile` to select another config source. The config
must select a valid `knowl.provider` from `runtime.providers`; provider startup
is lazy and begins when ingest planning needs it. See [local
operations](docs/operations.md) for the complete Balda-compatible provider and
typed SQLite/PostgreSQL examples.

The `ingest` and `lint` Cobra command names are present for the command
contract; the current operator workflows are the HTTP API and the public Go
packages. Plain embedders can construct a host through root `pkg/knowl`; Fx
embedders can use `pkg/knowlfx.NewApp`. The standalone host uses the selected
shared runtime provider for maintenance planning; deterministic embedding tests
can inject an explicit maintainer.

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

```bash
GOCACHE=/tmp/knowl-test-cache go test ./...
GOCACHE=/tmp/knowl-race-cache go test -race ./...
go vet ./...
go build ./...
# Requires Docker/Podman; starts an isolated PostgreSQL container.
go test -tags=integration ./pkg/knowl/store/postgres -run TestStoreContractWithTestcontainers -count=1
```

The repository uses `go.uber.org/fx` in `pkg/knowlfx` and `modernc.org/sqlite`
for the SQLite adapter. Balda integration is deliberately adapter-only; Knowl
does not import Balda storage, sessions, transports, or memory policy. See
[architecture](docs/architecture.md) and
[integration boundaries](docs/integrations.md).
