# Product design

Knowl is a service-first LLM-wiki knowledge service. It accepts durable source
material, maintains a readable Markdown knowledge workspace with provenance,
and returns bounded grounded evidence to a host agent or application.

The default deployment is a sidecar service with SQLite. Agents use MCP; HTTP
is the deterministic control transport. Go applications may use Fx to run the
same host runtime in-process.

## Product boundary

Knowl supports these baseline use cases:

1. Bootstrap an existing Markdown wiki or Obsidian vault into a Knowl-owned
   workspace.
2. Ingest one text or URI source through one canonical pipeline.
3. Retrieve bounded evidence and provenance for a host query.
4. Read durable ingest-operation status.

Knowl is not session, user-fact, or temporal memory; agent orchestration; the
primary final-answer generator; a generic memory platform; or a multi-tenant
control plane.

### Public business contract

The contract has exactly three business operations:

- retrieve bounded evidence;
- ingest one source;
- read one durable operation status.

MCP is the primary agent-facing interface:

- `knowl_retrieve`
- `knowl_ingest`
- `knowl_operation`

HTTP/OpenAPI provides the same deterministic contract:

- `GET /v1/retrieve`
- `POST /v1/ingest`
- `GET /v1/operations/{operation_id}`

`GET /healthz` and `GET /readyz` are operational endpoints, not additional
business operations. The authoritative HTTP schema is
[api/openapi/knowl.yaml](../api/openapi/knowl.yaml).

Neither transport exposes direct page CRUD, raw workspace writes, search
sub-steps, or public review/apply choreography. The operator CLI is a local
convenience wrapper over the same operations, not a primary agent interface.

## Content and trust boundaries

The filesystem workspace is the canonical content owner:

- `raw/` stores immutable accepted source versions;
- `wiki/` stores the human-readable Markdown knowledge artifact;
- `schema.md` is operator-owned policy;
- `wiki/index.md` and `wiki/log.md` are maintained control files.

SQL and search state are operational and rebuildable, never canonical content.
The detailed filesystem contract is in [workspace.md](workspace.md).

A source adapter may translate text, a URI, origin, and idempotency hints into
one public ingest request. It must not write the workspace or SQL directly,
select an arbitrary trusted scope, or submit page IDs, Markdown paths, or
ready-made changesets.

The host owns session and user context, final-answer generation, tool
orchestration, and the mapping to the trusted Knowl scope. Public callers
cannot override that scope. Knowl returns bounded evidence and operation status
only.

The configured provider is an implementation detail. Knowl resolves
`knowl.provider` through the shared `runtime.providers` configuration and
validates returned plans before canonical mutation. Provider code does not
receive unrestricted filesystem authority.

## Supported usage surfaces

```text
Agent-facing data plane:     MCP
Deterministic host control:  HTTP/OpenAPI
Go in-process alternative:   Fx over the same runtime
Operator convenience:        cmd/knowl
Underlying business policy:  pkg/knowl/app
```

The supported Go imports are:

- `pkg/knowl` for plain-Go host composition;
- `pkg/knowlfx` for Fx lifecycle integration;
- `pkg/knowl/mcp` for the bounded MCP adapter;
- `pkg/knowl/types` for transport-neutral domain types when embedding needs
  them.

Everything else is implementation detail of a surface, not another product
API. For example, a Balda host connects through its external MCP configuration
and does not embed, start, configure, or persist Knowl itself.

## Architecture and ownership

Dependencies flow inward:

```text
entrypoints and surfaces -> composition -> adapters -> app policy -> shared types
```

Current ownership follows that direction:

```text
pkg/knowl/types       shared IDs and data shapes
pkg/knowl/wiki        wiki and frontmatter semantics
pkg/knowl/app         business policy and consuming ports
content/fs, store/*, provider
                       adapters for workspace, operational state, and provider
pkg/knowl/mcp         MCP adapter
internal/httpapi      HTTP adapter
internal/mcphttp      Streamable HTTP transport for MCP
internal/bootstrap    workspace bootstrap support
pkg/knowl             composition root and host API
pkg/knowlfx, cmd/knowl
                       Fx and CLI entrypoints
```

`pkg/knowl/types` has no Knowl-package dependencies. `pkg/knowl/app` owns
business policy and ports; adapters depend on it, not the reverse.
`pkg/knowl` is the only package that composes multiple adapters. Fx and the
CLI must not become second composition roots with separate business rules.

When code becomes difficult to read, split files within its package first.
Create a package only for a distinct usage surface, external technology
adapter, or shared semantic contract—not to shorten a file or prepare
hypothetical reuse. `.go-arch-lint.yml` protects these top-level boundaries.

## Invariants and non-goals

- Provenance is durable and inspectable.
- Local defaults are bounded and deterministic.
- Startup validates the workspace, initializes storage, performs recovery, and
  prepares projections before readiness.
- Canonical writes preserve one-writer ordering.
- Knowl does not promise automatic crawling or research, vector DB as
  canonical storage, implicit forgetting, binary/image understanding, Git
  push/sync, a broad CRUD/admin API, or shared multi-tenant security.
