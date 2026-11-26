# PostgreSQL and optional native vectors

PostgreSQL lexical search requires no pgvector extension. Native dense retrieval
is opt-in through the existing [embedding settings](search.md); Knowl never
installs an extension or falls back to the old Go full-vector ranking path.
SQLite instead bundles its CGO-free native capability.

Merge this storage fragment into your existing configuration, removing the
SQLite subsection and preserving maintainer/sources/authentication:

```yaml
knowl:
  storage:
    type: postgres
    postgres:
      dsn: 'postgres://KNOWL_RUNTIME@DB_HOST/KNOWL_DB?sslmode=verify-full'
```

Replace the placeholders and use your deployment's credential facilities. The
checked-in type has `dsn`, not a `dsn_env` setting. Keep real credentials/local
overrides out of source control; `.config/knowl/*.local.yaml` is local-only.
Embedding `api_key_env` configures the separate embedding credential, not SQL.

## Install and upgrade

Start Knowl normally with the selected PostgreSQL configuration. Before serving
requests or starting workers, Knowl applies all required Goose migrations and
validates the required schema. No separate Goose executable, SQL-directory
selection or history-table flags are needed. A required migration or schema
validation failure stops startup, even with lexical fallback configured.

For lexical-only operation, pgvector is not required. Before enabling embeddings,
a PostgreSQL operator must provision the extension once in this database using
the deployment's supported package/service procedure. For a default trusted
schema, the operator SQL is:

```sql
CREATE EXTENSION vector;
```

Knowl never installs the extension. If already provisioned, verify the existing
deployment instead of reinstalling. Knowl creates and upgrades its application
tables and constraints automatically; missing extension access or permission to
apply pending DDL is a startup error, not a reason to silently serve degraded.

For format upgrades, stop all writers/hosts, including old binaries, and back up
canonical/raw data and the database with its migration histories. Start the
compatible new binary under migration authority, let startup rebuild the derived
projection, and verify real semantic retrieval before resuming writers.
`/readyz` alone is not a dense guarantee: provider/projection failures after
successful schema initialization still follow the configured search policy.
Do not roll old and new writers concurrently across a format transition.

## Optional migration administration

To apply schema changes separately from application startup, stop writers/hosts
and back up first. Use the ordinary Knowl configuration and, when needed, its
`--config-dir` and `--profile` flags:

```sh
knowl migrate up
```

This applies all pending required migrations and exits without starting the
service, resolving model/embedding credentials, synchronizing sources or
rebuilding projections. The loaded configuration must still be valid;
`knowl.embeddings.enabled` determines whether native schema is required.
Use the same storage configuration mechanisms for the migration role as for
the runtime; no separate migration DSN setting is needed. Keep credentials out
of command arguments and source control. See the shared
[migration command contract](operations.md#database-schema-migrations).

Migration authority needs permission to create/use history tables and alter
owned application tables/constraints, plus access to the extension
schema/type/functions when embeddings are enabled. A runtime role can instead
start on a fully pre-applied, recognized current schema without DDL ownership or
extension-install authority. It needs ordinary Knowl table/sequence access,
schema usage/native function execution and SELECT on the required Goose
histories. Grant optional-history access after pre-application; never create an
empty history as a permission workaround.

## Internal migration compatibility

Opening a store still runs embedded Goose migrations. All new application
schema changes are numbered Goose SQL, not ad hoc initializer DDL or a custom
version protocol. The historical Go migration 19 remains registered by Knowl.

| Stream | Sources | Standard history | Current native transition |
|---|---|---|---|
| Base, always considered on Open | `pkg/knowl/store/postgres/migrations` plus historical Go migration 19 | `goose_db_version` | SQL migration 26 adds `native_format` and invalidates derived dense state; remains extension-free |
| Optional, only for enabled dense with verified pgvector | `pkg/knowl/store/postgres/pgvector_migrations` | `goose_pgvector_db_version` | SQL migration 1 replaces derived BYTEA vectors with extension-owned unbounded `vector` and a dimension constraint |

Knowl preserves these two released histories internally. Up runs base before
optional; optional Up is required when embeddings are enabled. Lexical-only
startup does not create the optional history. Operators do not select a stream.
Use the compatible Knowl binary: the base SQL directory alone omits historical
Go migration 19. Rebuild/query do not install the extension or repair history.

Dense admission resolves the extension's owned type/functions/operator from
catalogs, including a trusted nondefault extension schema; it does not trust a
same-named search-path object. Tested locally with PostgreSQL 16.14 and
pgvector 0.8.2. No ANN index or additional dimensions/capacity knob is required.

## Failure, corrupt history and recovery

Migration commands fail with a nonzero exit and no success JSON. Individual
transactional migrations roll back on failure, but earlier Up steps may already
have committed, including the base stream before a native-schema failure.
Fix the actual cause and retry startup or `knowl migrate up`; committed Goose
history determines the remaining work. This is different from an
**existing but empty** `goose_pgvector_db_version`: that is corrupt history,
not a fresh installation. Knowl fails closed with safe migration diagnostics
and leaves it unchanged. Unknown/duplicate/inconsistent history or an
unrecognized required native schema also stops initialization.

Keep hosts stopped and recover outside Knowl from trusted deployment evidence
or a known-good matching database/history restore. This guide deliberately
provides no blind repair SQL: do not insert a fabricated version 0, drop/recreate
history, use `-no-versioning`, or add an alternate bootstrap. After supported
operator recovery, ordinary startup or `knowl migrate up` applies pending work
and validates the required schema. Restart and rebuild; queries do not perform
automatic repair.
Preserve canonical pages, raw evidence, source state and durable operations.

## Rollback order

Stop all writers/hosts first, back up canonical/raw data and the database, and
select a compatible binary/configuration. A deliberate one-step rollback is:

```sh
knowl migrate down
```

Knowl reverses exactly one migration per call: optional first if it has applied
migrations, otherwise base. This ordering also applies when embeddings are now
disabled. No applied migration means a successful no-op. Down never runs Up.

Optional migration 1 Down invalidates derived dense state and returns the vector
column to BYTEA. Base SQL migration 26 refuses the wrong order while native type
remains.
Neither of those rollback steps uninstalls the operator's extension nor deletes
canonical/raw/source/operation data. Other historical Down steps can discard
schema/data; review the intended step before each call. Schema rollback is
not vector reuse: pair the schema with a compatible binary and rebuild before
resuming. Starting the current binary again automatically reapplies required
pending Up migrations, so do not restart it midway through an intended downgrade.
Migration advisory locks coordinate migrators, not live application writes.

Publication, reuse and recovery fully validate the bounded projection before
atomic readiness; page changes invalidate incompatible state. Ordinary reads
admit scalar ready/snapshot/model state and retain consumed-evidence validation,
with exhaustive eligible native cosine scoring and no separate full-population
integrity audit. Per-read detection of arbitrary out-of-band corruption in
nonselected rows is outside the application-owned SQL trust boundary.
Read [the actual comparison](testing/native-vectors.md): current latency results
remain mixed, including PostgreSQL regressions with source filtering, while
original v1.3 regressions remain visible. Zero stored-vector transfer and lower
Go allocation volume do not guarantee faster queries. Isolated 100000-chunk
kernels do not promise production capacity or faster complete queries.
