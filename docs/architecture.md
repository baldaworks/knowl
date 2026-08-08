# Knowl architecture

Knowl has one canonical content owner: the local workspace. `raw/` contains immutable accepted source versions; `wiki/` contains the human-readable Markdown knowledge artifact; `schema.md` is operator-owned policy; `wiki/index.md` and `wiki/log.md` are maintained workspace files. SQL is never canonical content.

The public package map is:

```text
pkg/knowl/                         domain IDs and data-only types
pkg/knowl/app/                     application policy and consuming ports
pkg/knowl/content/fs/              canonical filesystem adapter
pkg/knowl/store/sqlite/            SQLite operations/projections
pkg/knowl/store/postgres/          PostgreSQL operations/projections
pkg/knowl/provider/                maintainer provider boundary
pkg/knowl/mcp/                     bounded read-only MCP adapter
internal/apps/knowl/               local composition, config, worker, lifecycle
```

`pkg/knowl/app` owns the interfaces. Concrete adapters depend inward on those interfaces. The public core has no Balda, Cobra, Viper, Goose, SQL-driver, provider-SDK, HTTP, or MCP-broker dependency. Balda integration remains an adapter that maps Balda-owned material into a Knowl source envelope and consumes bounded Knowl results.

The SQL adapters implement `OperationStore` and `SearchIndex`; they retain operations, leases, redacted status, provenance indexes, and rebuildable text/link projections. Canonical Markdown and immutable raw sources remain in the filesystem adapter.
