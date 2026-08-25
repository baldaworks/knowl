# Product design

**Durable project knowledge for agents.**

Knowl is a self-hosted knowledge sidecar for agentic applications. It turns
durable sources into an inspectable Markdown knowledge base and returns
bounded, provenance-backed evidence.

The host decides which events are durable, assigns their immutable source
revisions, orchestrates tools, and generates the final user answer. Knowl owns
durable acceptance, canonical raw and Markdown artifacts, operation recovery,
and bounded evidence with source references. The configured maintainer
provider proposes validated Markdown updates inside Knowl; it is neither a
connector nor another public interface.

The default deployment is a sidecar service with SQLite. Agents use MCP; HTTP
is the deterministic control transport. Go applications may use Fx to run the
same host runtime in-process.

## Product boundary

Knowl supports these baseline use cases:

1. Bootstrap an existing Markdown wiki, Obsidian vault, or OKF v0.2 bundle into
   a Knowl-owned workspace as the first production filesystem-source sync.
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
- `wiki/` stores the human-readable, directly portable OKF v0.2 bundle;
- `wiki/sources/<source_id>/**` stores active read-only source mirrors and is
  writable only by the source reconciler;
- `schema.md` is operator-owned policy;
- `wiki/index.md` declares the bundle version and, with every reserved
  `index.md`/`log.md`, is control content rather than retrieval evidence.

SQL and search state are operational and rebuildable, never canonical content.
The detailed filesystem contract is in [workspace.md](workspace.md).

An ingest-side connector may translate text, a URI, origin, and idempotency
hints into one public ingest request. Separately, the built-in read-only
filesystem source adapter lists and fetches configured wiki documents for the
source reconciler. Neither may write the workspace or SQL directly, select an
arbitrary trusted scope, or submit ready-made canonical changesets.

The host owns session and user context, final-answer generation, tool
orchestration, and the mapping to the trusted Knowl scope. Public callers
cannot override that scope. Knowl returns bounded evidence and operation status
only.

Source-system ownership stays outside Knowl. A Balda, Norma, or equivalent host
decides when an ADR, completed story, investigation, issue, pull request, or
runbook has become durable and submits that immutable revision. Knowl does not
own Slack, Telegram, Jira, GitHub, or their workflows, and it does not answer
the user itself.

An optional configured provider is an implementation detail. When present,
Knowl resolves `knowl.provider` through the shared `runtime.providers`
configuration and validates returned plans before canonical mutation. Without
one, deterministic reads, lint, source synchronization, status, health, HTTP,
and MCP reads remain available; ingest fails before accepting or reserving work.
Provider code does not receive unrestricted filesystem authority.

OKF Attested Computation declarations are data, not an execution interface.
Knowl preserves and exposes their runtime, parameters, computation, executor,
and attester fields without loading resources or running any declared program.

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
internal/source       filesystem adapter, normalization, and reconciliation
pkg/knowl             composition root and host API
pkg/knowlfx, cmd/knowl
                       Fx and CLI entrypoints
```

`pkg/knowl/types` has no Knowl-package dependencies. `pkg/knowl/app` owns
business policy and ports; adapters depend on it, not the reverse.
`pkg/knowl` is the only package that composes multiple adapters. Fx and the
CLI must not become second composition roots with separate business rules.

Bootstrap is deliberately only a CLI preflight: it checks freshness and path
separation, initializes the local workspace/config, constructs one deterministic
filesystem source, and calls `Host.SyncSource` once. Ordinary sync has no
freshness rule, never replaces `wiki/index.md`, and converges only the exact
`wiki/sources/<source_id>/**` namespace. Sidecar and embedded callers use this
same Host engine and durable recovery order.

Canonical-format migration is similarly explicit: `knowl migrate okf-v0.2`
preflights and journals the conversion, preserves the exact legacy log in an
archive, commits a marker last, and rebuilds projections. Host startup and
read-only commands reject legacy canonical state rather than changing it.

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
