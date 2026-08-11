# Knowl architecture

Knowl has one canonical content owner: the workspace. `raw/` contains immutable
accepted source versions; `wiki/` contains the human-readable Markdown
knowledge artifact; `schema.md` is operator-owned policy; `wiki/index.md` and
`wiki/log.md` are maintained workspace files. SQL is never canonical content.

The public package map is:

```text
pkg/knowl/types/                   domain IDs and data-only types
pkg/knowl/wiki/                    shared wiki/frontmatter semantics
pkg/knowl/app/                     application policy and consuming ports
pkg/knowl/                         plain-Go host composition API
pkg/knowlfx/                       Fx lifecycle wrapper over pkg/knowl
pkg/knowl/content/fs/              canonical filesystem adapter
pkg/knowl/store/sqlite/            SQLite operations/projections
pkg/knowl/store/postgres/          PostgreSQL operations/projections
pkg/knowl/provider/                maintainer provider boundary
pkg/knowl/mcp/                     bounded three-tool MCP adapter
```

`pkg/knowl/app` owns the interfaces. Concrete adapters depend inward on those interfaces. The public core has no Balda, Cobra, Viper, Goose, SQL-driver, provider-SDK, HTTP, or MCP-broker dependency. Balda integration remains an adapter that maps Balda-owned material into a Knowl source envelope and consumes bounded Knowl results.

The SQL adapters implement `OperationStore` and `SearchIndex`; they retain operations, leases, redacted status, provenance indexes, and rebuildable text/link projections. Canonical Markdown and immutable raw sources remain in the filesystem adapter.

Root `pkg/knowl` is the source of truth for non-Fx host assembly. Its
constructors perform workspace validation, selected-store migration,
interrupted-filesystem recovery, and projection readiness before the host
becomes ready. `pkg/knowlfx.NewApp` and `pkg/knowlfx.Module` give Fx the same
host lifecycle without re-implementing assembly: startup binds the configured
HTTP listener and starts the bounded single-consumer writer; shutdown marks the
host unready, drains or interrupts queued work, recovers the workspace, and
closes the operational store. The worker preserves one-writer ordering for
canonical files.

The transport boundary is intentionally narrow. Both MCP and HTTP expose the
same three business operations: retrieve, ingest, and operation status. Health
and readiness stay operational-only. Sidecar/service deployment is the baseline runtime shape;
Fx remains the in-process alternative over the same app core.

See [workspace semantics](workspace.md), [service operations](operations.md),
and [integration boundaries](integrations.md).
