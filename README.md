# Knowl

Knowl is an independent, local-first Markdown knowledge wiki. It turns bounded,
curated source revisions into a human-readable, cited wiki while keeping the
filesystem workspace as the source of truth.

The core is embeddable under `pkg/knowl`. The local `knowl` process composes the
filesystem adapter, SQLite or PostgreSQL operational state, a serialized writer,
the loopback HTTP operator API, and bounded read-only MCP tools. The host uses
Uber Fx for lifecycle ownership; canonical application policy remains in
`pkg/knowl/app`.

## Quick start

```bash
go build -o knowl ./cmd/knowl
./knowl --workspace ./knowledge init
./knowl --workspace ./knowledge validate
./knowl --workspace ./knowledge start
```

`start` listens on `127.0.0.1:8080` by default. Configuration is loaded from
`.config/knowl/config.yaml`, then `KNOWL_*` environment overrides, with the
`--workspace`, `--store-driver`, and `--config` flags available from Cobra. See
[local operations](docs/operations.md) for a complete configuration example and
the HTTP/MCP surfaces.

The `ingest` and `lint` Cobra command names are present for the command contract;
the current operator workflows are the HTTP API and the public Go packages. A
standalone host started by the CLI is read-only until an independent maintainer
is supplied through the embedding boundary; this avoids silently selecting a
model, endpoint, credential, or reasoning policy.

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

The repository uses `go.uber.org/fx` for the host composition root and
`modernc.org/sqlite` for the SQLite adapter. Balda integration is deliberately
adapter-only; Knowl does not import Balda storage, sessions, transports, or
memory policy. See [architecture](docs/architecture.md) and
[integration boundaries](docs/integrations.md).
