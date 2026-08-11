# Integration boundaries

Knowl is a durable knowledge service. Its job is to turn accepted sources into
grounded evidence over a readable Markdown knowledge workspace.

Knowl is not:

- session memory;
- user-fact or temporal memory;
- agent orchestration;
- the primary final-answer generator;
- an automatic multi-tenant control plane.

## Interface hierarchy

```text
Agent-facing data plane:     MCP
Deterministic host control:  HTTP/OpenAPI
Go in-process alternative:   Fx over the same runtime
Underlying business logic:   pkg/knowl/app services
```

## Agent-facing boundary

Generic agents should integrate through MCP first.

Baseline MCP surface:

- `knowl_retrieve`
- `knowl_ingest`
- `knowl_operation`

The standalone service exposes this surface as MCP Streamable HTTP at `/mcp`
on its existing service listener.

The equivalent HTTP contract is:

- `GET /v1/retrieve`
- `POST /v1/ingest`
- `GET /v1/operations/{operation_id}`

Neither MCP nor HTTP exposes direct page CRUD, raw workspace writes, search
sub-steps, or mandatory public review/apply choreography.

## Source adapter boundary

A source adapter may translate external material into one public ingest request.
It may supply:

- text content;
- a URI;
- stable origin and idempotency hints.

It must not:

- write `raw/`, `wiki/`, `.knowl/`, or SQL directly;
- choose arbitrary trusted scope per request;
- bypass the ingest pipeline with page IDs, Markdown paths, or ready-made
  changesets.

## Host/application boundary

The host owns:

- session context;
- user context;
- final answer generation;
- tool orchestration and policy;
- mapping between its own identity model and a trusted Knowl scope.

Knowl returns bounded evidence and durable operation status. The host decides
how to combine that evidence with everything else it knows.

## Balda and adjacent systems

Balda supports Knowl through its generic external MCP configuration:

```yaml
runtime:
  mcp_servers:
    knowl:
      type: http
      url: http://127.0.0.1:8080/mcp

balda:
  mcp_servers:
    - knowl
```

Knowl is started and configured separately. Balda does not embed Knowl, manage
its process, initialize its workspace, or own its provider and persistence.
Remove `knowl` from `balda.mcp_servers` to stop injecting it into new Balda
sessions.

Balda or another host runtime may:

- translate project/domain material into Knowl ingest requests;
- consume Knowl evidence through MCP or HTTP;
- keep its own session memory, user memory, and orchestration separately.

There is no automatic Balda-turn ingestion. Content enters Knowl only through
an explicit `knowl_ingest` call or another operator-controlled ingest path.

Knowl must not import or directly own those other persistence layers.

## Provider boundary

The selected runtime provider is an implementation detail behind the maintainer
adapter.

Knowl:

- reads `knowl.provider`;
- resolves it through the shared `runtime.providers` document;
- invokes the provider lazily through the maintainer boundary;
- validates returned plans before canonical mutation.

Provider code does not receive unrestricted filesystem authority.

## Out of scope

The accepted baseline does not promise:

- automatic web research or crawling;
- vector DB as canonical storage;
- shared multi-tenant security guarantees;
- implicit forgetting or deletion of immutable raw sources;
- binary/image understanding;
- automatic Git push/sync;
- a broader CRUD/admin public API.
