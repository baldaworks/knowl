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
