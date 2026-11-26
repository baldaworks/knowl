# HTTP facade access control

Knowl configures its HTTP API, Streamable HTTP MCP endpoint, and optional Web
UI independently. Authentication decides who may reach a facade; access mode
decides which capabilities that facade publishes.

| Facade | `auth_mode` | `access_mode` |
| --- | --- | --- |
| `api` | `public`, `token` | `read_only`, `read_write` |
| `mcp` | `public`, `token` | `read_only`, `read_write` |
| `web` | `public`, `token`, `proxy` | `read_only` |

An omitted `auth_mode` becomes `token` when `operator.token` is nonempty and
`public` otherwise. An omitted `access_mode` always becomes `read_only`.
`read_write` must be explicit and is valid only with `token` authentication.
`public` plus `read_write`, proxy authentication on API/MCP, and a read-write
Web UI are rejected during configuration validation. Web remains disabled
unless `web.enabled: true` is set.

> **Upgrade warning:** a service that previously set only `operator.token`
> still requires that token after upgrading, but API and MCP silently become
> read-only. Add `access_mode: read_write` explicitly to each facade that must
> retain ingestion. This fail-safe downgrade is intentional.

## Deployment profiles

### Public Web and authenticated MCP

This publishes only the Web read projection while keeping MCP authenticated and
read-only. The two facades do not inherit policy from each other.

```yaml
knowl:
  operator:
    token: ${KNOWL_OPERATOR_TOKEN}
  api:
    auth_mode: token
    access_mode: read_only
  mcp:
    auth_mode: token
    access_mode: read_only
  web:
    enabled: true
    auth_mode: public
    access_mode: read_only
```

Native MCP read-only mode registers only `knowl_retrieve` and
`knowl_operation`. `knowl_ingest` is absent from `tools/list` and cannot be
called directly. Set MCP to explicit `token` plus `read_write` for a trusted
internal client that must ingest.

### Shared external MCP through Pomerium

For one Knowl endpoint serving a trusted internal writer and external users,
configure Knowl as token-authenticated read-write and keep its listener on a
private network. Internal clients use the private URL and bearer directly.
Pomerium authenticates external users through an OIDC provider such as
Keycloak, overwrites the upstream `Authorization` header with the fixed Knowl
operator bearer, and enforces reader/writer policy on MCP `tools/call`.

```yaml
knowl:
  operator:
    token: ${KNOWL_OPERATOR_TOKEN}
  mcp:
    auth_mode: token
    access_mode: read_write
  web:
    enabled: false
```

The operator bearer is service-to-service material: inject it into both
processes from secret storage, never expose it to browsers or external MCP
clients, and configure the proxy to replace rather than preserve any client
`Authorization` header. Do not expose the Knowl origin outside the trusted
network. Terminate TLS at the gateway.

Pomerium tool policy denies `knowl_ingest` for reader identities and allows it
for writers. This call denial does **not** rewrite MCP discovery: readers can
still see `knowl_ingest` in `tools/list`, but an attempted call is rejected by
the gateway. Use Knowl's native `mcp.access_mode: read_only` when the write tool
must be omitted from discovery; that setting also removes write access from
trusted direct clients of the same endpoint.

### Web authentication through Pomerium

Proxy Web mode is independent from MCP and API policy:

```yaml
knowl:
  operator:
    token: ${KNOWL_OPERATOR_TOKEN}
  mcp:
    auth_mode: token
    access_mode: read_write
  web:
    enabled: true
    auth_mode: proxy
    access_mode: read_only
```

Protect the Web routes at Pomerium and overwrite the upstream bearer with the
fixed operator token. The browser receives neither the operator token nor a
Knowl credential form. On an expired or redirected proxy session, the UI
rejects the response body and offers a reconnect to the current same-origin UI
URL; the gateway owns the subsequent login flow. Cookies, user lifecycle,
groups, MFA, and RBAC stay in Keycloak/Pomerium rather than Knowl.

The token-mode Web UI remains suitable for trusted direct access: shell and
assets are public bootstrap resources, data fragments require the in-memory
bearer, and reload or disconnect clears it. In every Web mode the presentation
is read-only.
