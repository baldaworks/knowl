# Knowl

**Durable project knowledge for agents.**

Knowl is a self-hosted knowledge sidecar for agentic applications. It turns
durable sources into an inspectable Markdown knowledge base and returns
bounded, provenance-backed evidence.

The ownership boundary is deliberate:

| Component | Owns |
| --- | --- |
| Host agent or application | Deciding which events are durable, assigning immutable source revisions, orchestrating tools, and generating the final user answer. |
| Knowl | Accepting durable sources, maintaining canonical raw and Markdown artifacts, resuming operations, and retrieving bounded evidence with source references. |
| Maintainer provider | Proposing Markdown updates inside Knowl's validated write path; it is an implementation detail, not a connector or public interface. |

Knowl is not:

- session memory;
- user-fact or temporal memory;
- workflow orchestration;
- the primary final-answer generator.

Knowl does not own Slack, Telegram, Jira, GitHub, or other source connectors.
The host already knows which events should become durable and submits those
events through `knowl_ingest`. Knowl does not answer the user itself.

The intended shape is:

```text
sources -> Knowl -> grounded evidence -> host agent -> final answer
```

See the [project-decisions host example](examples/project-decisions/README.md)
for the complete ADR ingest, operation polling, provenance retrieval, and
host-owned answer flow over MCP.

The default path is a sidecar service with SQLite. Connect agents over MCP;
use HTTP for deterministic control and Fx only when a Go process needs the
same runtime in-process.

## When to use it

Use Knowl when you want one durable project/domain knowledge layer that can:

- bootstrap an existing Markdown wiki or Obsidian vault into a Knowl-owned
  workspace;
- ingest new text or URI sources through one canonical pipeline;
- answer retrieval requests with bounded evidence and provenance;
- run next to an agent as a sidecar service or inside a Go process through Fx.

Typical examples:

- “I already have an internal wiki and want an agent to read it safely.”
- “I want new findings from chat, tickets, or URLs to become durable project
  knowledge.”
- “I need the same knowledge service to work for MCP agents, HTTP clients, and
  Go embedding.”

## Public product shape

Knowl has one business contract with three operations.

Primary agent-facing interface: MCP

- `knowl_retrieve`
- `knowl_ingest`
- `knowl_operation`

Equivalent deterministic HTTP/OpenAPI transport:

- `GET /v1/retrieve`
- `POST /v1/ingest`
- `GET /v1/operations/{operation_id}`

Operational endpoints:

- `GET /healthz`
- `GET /readyz`

The business semantics are the same across MCP and HTTP:

- retrieve bounded evidence;
- ingest one source;
- poll one durable operation.

## Deployment modes

Baseline: sidecar service

- build the image from [Dockerfile](Dockerfile);
- use [deploy/sidecar/knowl.yaml](deploy/sidecar/knowl.yaml) as the baseline
  container config;
- start from [deploy/sidecar/compose.yaml](deploy/sidecar/compose.yaml) for the
  minimal local example;
- mount persistent storage at `/var/lib/knowl`.

See [docs/sidecar.md](docs/sidecar.md).

Alternative: Go embedding with Fx

- root `pkg/knowl` is the plain-Go host/runtime composition layer;
- `pkg/knowlfx` is the Fx lifecycle wrapper over the same runtime;
- both modes call the same application services and storage contracts.

## Quick start

Build the CLI:

```bash
go build -o knowl ./cmd/knowl
```

Bootstrap an existing wiki:

```bash
./knowl bootstrap wiki /path/to/existing/wiki
```

Or initialize an empty local workspace:

```bash
./knowl init
./knowl validate
```

Start the service:

```bash
./knowl start
curl -sS http://127.0.0.1:8080/readyz
```

The same listener exposes MCP Streamable HTTP at
`http://127.0.0.1:8080/mcp`.

Run one-shot local wrappers over the same KISS contract:

```bash
./knowl retrieve "Why was Badger chosen?"
./knowl ingest --input request.json
./knowl operation op_01K...
```

These CLI commands are operator conveniences. They are not the primary product
story for agent integration.

## Example ingest request

```json
{
  "content": "Badger was chosen for session memory because ...",
  "origin": "ticket-1234",
  "idempotency_key": "ticket-1234"
}
```

Or:

```json
{
  "uri": "https://example.com/adr/session-memory-store"
}
```

The public ingest request does not expose page IDs, Markdown paths, or raw
workspace mutation.

## Configuration shape

Knowl config lives under the `knowl:` section and stays aligned with Balda's
typed runtime/provider shape.

Minimal SQLite example:

```yaml
runtime:
  providers:
    opencode:
      type: opencode_acp
      opencode_acp:
        model: opencode/big-pickle

knowl:
  provider: opencode
  workspace:
    path: .
  storage:
    type: sqlite
    sqlite:
      path: .knowl/knowl.sqlite
  operator:
    token: replace-with-a-local-secret
```

Container baseline example:

```yaml
knowl:
  workspace:
    path: /var/lib/knowl/knowledge
  server:
    listen_addr: 0.0.0.0:8080
```

Detailed config and service guidance live in [docs/operations.md](docs/operations.md).
When an operator token is configured, business HTTP and MCP requests require an
`Authorization: Bearer <token>` header; health probes remain public.

## Repository layout

Inside the workspace root:

```text
workspace/
├── schema.md
├── raw/
├── wiki/
│   ├── index.md
│   ├── log.md
│   ├── entities/
│   ├── concepts/
│   └── syntheses/
└── .knowl/
    ├── staging/
    ├── recovery/
    └── knowl.sqlite
```

`raw/` and `wiki/` are canonical knowledge artifacts. SQL state and projections
remain rebuildable operational state.

## Public packages

- `pkg/knowl/types` — transport-neutral domain types
- `pkg/knowl` — plain-Go host/runtime composition
- `pkg/knowlfx` — Fx lifecycle wrapper over `pkg/knowl`
- `pkg/knowl/mcp` — the three-tool MCP adapter

## Where to look next

- sidecar/service runbook: [docs/sidecar.md](docs/sidecar.md)
- v0.1.0 release and rollback notes: [docs/releases/v0.1.0.md](docs/releases/v0.1.0.md)
- service config and HTTP contract: [docs/operations.md](docs/operations.md)
- product design, boundaries, and architecture: [docs/design.md](docs/design.md)
- workspace semantics: [docs/workspace.md](docs/workspace.md)
- authoritative HTTP contract: [api/openapi/knowl.yaml](api/openapi/knowl.yaml)

## Development

Regenerate checked-in HTTP bindings after contract changes:

```bash
go tool oapi-codegen -config api/openapi/oapi-codegen.yaml api/openapi/knowl.yaml
```

Primary repository checks:

```bash
go test ./...
go tool golangci-lint run ./...
```
