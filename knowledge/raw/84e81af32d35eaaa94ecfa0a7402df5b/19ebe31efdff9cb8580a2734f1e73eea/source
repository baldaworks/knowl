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

## Two standard Goose streams

Opening a store still runs embedded Goose migrations. All new application
schema changes are numbered Goose SQL, not ad hoc initializer DDL or a custom
version protocol. The historical Go migration 19 remains registered by Knowl.

| Stream | Sources | Standard history | Current native transition |
|---|---|---|---|
| Base, always considered on Open | `pkg/knowl/store/postgres/migrations` plus historical Go migration 19 | `goose_db_version` | SQL migration 26 adds `native_format` and invalidates derived dense state; remains extension-free |
| Optional, only for enabled dense with verified pgvector | `pkg/knowl/store/postgres/pgvector_migrations` | `goose_pgvector_db_version` | SQL migration 1 replaces derived BYTEA vectors with extension-owned unbounded `vector` and a dimension constraint |

Up order is **base before optional**. An unavailable extension leaves optional
SQL pending, not falsely recorded as applied; lexical-only operation remains
usable and does not create the optional history. After operator provisioning,
reopen with embeddings enabled to retry pending SQL, then rebuild the derived
projection. Rebuild/query do not install the extension or repair migration
history. A cached unavailable admission is not fixed by querying again.

Dense admission resolves the extension's owned type/functions/operator from
catalogs, including a trusted nondefault extension schema; it does not trust a
same-named search-path object. Tested locally with PostgreSQL 16.14 and
pgvector 0.8.2. No ANN index or additional dimensions/capacity knob is required.

## Provision and pre-apply in a maintenance window

Stop **all** writers/hosts, including old binaries. Back up canonical/raw data
and the database with both migration histories. Use a compatible Knowl binary
under migration authority to initialize/upgrade the base stream through 26;
its ordinary Open registers historical Go migration 19. A standalone Goose
SQL-directory invocation is **not a replacement for fresh base initialization** or that Go
migration. Do not concurrently run CLI migrations with Knowl or other migrators.

Separately, an operator with appropriate PostgreSQL authority installs pgvector
in this database using its supported package/service procedure. For a default
trusted schema, the operator SQL is:

```sql
CREATE EXTENSION vector;
```

This is operator provisioning outside Knowl, not application startup SQL. If
already provisioned, verify the existing deployment instead of reinstalling.
Do not put extension installation into Knowl's base migration directory.

For the optional **SQL-only** stream, ordinary Goose CLI templates are valid
after base initialization. Use a Goose CLI compatible with the checked-in
v3.27.3 library, from this checkout. Supply `GOOSE_DBSTRING` securely in the
operator environment for the intended database/migration role; no DSN value is
shown in these commands. Disable shell tracing and treat migration tool logs
as sensitive. `-env none` avoids unexpectedly loading another `.env` file.
Use these templates only for a genuinely absent fresh history or recognized
valid existing history, never as a repair for an already-empty/corrupt table.

```sh
GOOSE_DRIVER=postgres goose -env none \
  -dir pkg/knowl/store/postgres/pgvector_migrations \
  -table goose_pgvector_db_version status
GOOSE_DRIVER=postgres goose -env none \
  -dir pkg/knowl/store/postgres/pgvector_migrations \
  -table goose_pgvector_db_version up
```

Migration authority needs permission to create/use the standard history and
alter the owned embedding tables/constraints, plus access to the extension
schema/type/functions. A runtime role can instead use a fully pre-applied,
recognized current history: it needs its ordinary Knowl table/sequence access,
schema usage/native function execution and **SELECT on both Goose histories**,
including `goose_pgvector_db_version`, but not ownership or extension-install
authority. If the optional history did not exist, grant its SELECT only after
pre-application; do not create an empty history as a permission workaround.
Denied pending DDL remains typed `unavailable` or configured lexical degradation
until a privileged operator pre-applies it and Knowl is reopened.

With the matching new binary, reopen, explicitly rebuild or let the documented
startup rebuild complete, then check real semantic retrieval reports/evidence.
`/readyz` alone is not a dense guarantee. Resume writers only after verification.
Do not roll old and new writers concurrently across this format transition.

## Failure, corrupt history and recovery

Ordinary transactional optional SQL failure rolls back schema/data/history;
if a valid standard version 0 history remains, remove the actual external cause
and ordinary Up on a later reopen can retry. This is different from an
**existing but empty** `goose_pgvector_db_version`: that is corrupt history,
not a fresh installation. Knowl fails closed with a redacted typed `unavailable`
reason and leaves it unchanged. Unknown/duplicate/inconsistent history or an
unrecognized schema also cannot be admitted as ready native state.

Keep hosts stopped and recover outside Knowl from trusted deployment evidence
or a known-good matching database/history restore. This guide deliberately
provides no blind repair SQL: do not insert a fabricated version 0, drop/recreate
history, use `-no-versioning`, or add an alternate bootstrap. After supported
operator recovery, ordinary Goose Up and read-only history/schema validation
govern admission. Reopen and rebuild; queries do not perform automatic repair.
Preserve canonical pages, raw evidence, source state and durable operations.

## Rollback order

Stop writers and back up first. Roll the optional stream Down **before** base
SQL migration 26 Down. For optional current version 1, the one-step template is:

```sh
GOOSE_DRIVER=postgres goose -env none \
  -dir pkg/knowl/store/postgres/pgvector_migrations \
  -table goose_pgvector_db_version down
```

Optional Down invalidates derived dense state and returns the vector column to
BYTEA. Base SQL migration 26 refuses the wrong order while native type remains.
Use a matching Knowl migration provider/binary for the required base direction,
especially if crossing historical Go migration 19; do not treat the SQL directory
as a complete standalone base migrator. Neither rollback step uninstalls the operator's
extension nor deletes canonical/raw/source/operation data. Schema rollback is
not vector reuse: pair the schema with a compatible binary and rebuild before
resuming. Do not reopen the new dense-enabled binary midway through rollback,
because ordinary optional Up can apply the pending transition again.

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
