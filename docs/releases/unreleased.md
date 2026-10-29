# Unreleased

## HTTP facade policy migration

API, Streamable HTTP MCP, and the optional Web UI now have independent
authentication and capability policy. `auth_mode` defaults to `token` when an
operator token exists and `public` otherwise. `access_mode` always defaults to
`read_only`.

This changes existing service behavior intentionally: a configuration that sets
`operator.token` but omits API/MCP `access_mode` still requires its bearer, but
ingestion is disabled after upgrade. Add the following only to facades that
must remain writable:

```yaml
knowl:
  api:
    auth_mode: token
    access_mode: read_write
  mcp:
    auth_mode: token
    access_mode: read_write
```

Public write configurations are invalid. Back up the workspace and operational
database before upgrade, keep the origin private when using an identity-aware
proxy, and verify retrieve, operation and ingest behavior for every enabled
facade. See [HTTP facade access control](../access-control.md) for deployment
profiles and the Pomerium discovery limitation.

## Database statistics page

The optional Web UI now includes a read-only **Statistics** page. Each open or
manual refresh reads the current database state; Knowl does not persist the
reading, retain history, poll, or render graphs. Scope-bound product counts are
shown separately from deployment-wide database and fixed-table storage facts.

SQLite and PostgreSQL are supported. Backend-specific size details are marked
unavailable when the SQLite `dbstat` facility or PostgreSQL catalog permission
is absent, while the remaining counts continue to render. The page follows the
existing Web authentication policy. This is an additive feature: it changes no
configuration, schema, API, or MCP contract and requires no migration.
