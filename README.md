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

- optionally bootstrap an existing Markdown wiki, Obsidian vault, or OKF v0.2
  bundle as raw evidence through the production source synchronization engine;
- combine multiple named read-only filesystem sources into one deduplicated,
  maintainer-owned semantic wiki;
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

Optionally bootstrap an existing wiki into a fresh workspace:

```bash
./knowl bootstrap wiki /path/to/existing/wiki
# or preserve an existing Open Knowledge Format v0.2 bundle
./knowl bootstrap okf /path/to/okf-bundle
```

Bootstrap is a freshness-guarded first sync, not a startup requirement. It
creates the deterministic `bootstrap-wiki` source (or `bootstrap-obsidian` /
`bootstrap-okf`), stores its exact documents under `raw/`, and queues durable
maintenance operations. Source documents are never copied into `wiki/`.
The generated local config includes a maintainer provider because every
runnable host must be able to turn accepted text into semantic OKF pages.

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
./knowl source list
./knowl source sync engineering
./knowl source status engineering
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
typed runtime/provider shape. A runnable host requires either an explicitly
injected maintainer or a `knowl.provider` entry resolved from
`runtime.providers`; invalid or absent provider configuration fails before
readiness.

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

Configure one or more sources alongside the required provider. Initial
bootstrap and automatic `on_start` synchronization are both optional; sources
can instead be synchronized explicitly with `knowl source sync`.

```yaml
knowl:
  provider: opencode
  workspace:
    path: .
  sources:
    - id: engineering
      type: filesystem
      filesystem:
        root: /wikis/engineering
        include: ["**/*.md"]
        flavor: obsidian
      sync:
        on_start: false
        interval: 5m
    - id: operations
      type: filesystem
      filesystem:
        root: /wikis/operations
        include: ["**/*.md"]
        flavor: markdown
      sync:
        on_start: false
        interval: 5m
    - id: catalog
      type: filesystem
      filesystem:
        root: /knowledge/catalog
        include: ["**/*"]
        flavor: okf
      sync:
        on_start: false
        interval: 5m
  storage:
    type: sqlite
    sqlite:
      path: .knowl/knowl.sqlite
```

Source IDs are part of document lineage. Equal paths such as `Shared.md` are
stored as independent immutable revisions under `raw/`; the maintainer may
synthesize their related facts into one semantic page carrying both source
documents. Repeated syncs fetch no unchanged bytes. Complete scans tombstone
deletions while raw history and previously curated knowledge remain.

A successful source sync means raw acceptance plus durable maintenance
reservation. LLM maintenance runs asynchronously; `source status` reports its
bounded queued, replayed, committed, and failed counts and samples separately.

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
│   ├── catalogs/**/index.md
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

`wiki/` itself is a portable Open Knowledge Format v0.2 bundle. Its root
`index.md` declares `okf_version: "0.2"`; ordinary Markdown concepts retain
standard OKF metadata and unknown extension fields. Retrieval exposes that
metadata as a structured `okf` object over CLI, HTTP, and MCP. Reserved
`index.md` and `log.md` files are control documents, not search evidence.
Configured source files are not part of this portable bundle. On the next
successful reconciliation, legacy derived `wiki/sources/<source_id>/**`
content is removed through the staged recovery mechanism without changing raw
history or curated pages.

An existing valid flat semantic wiki remains readable and is never reorganized
by startup, bootstrap, validation, retrieval, source status, or source listing.
To explicitly build or refresh source-independent nested catalogs, stop other
writers and run:

```bash
./knowl hierarchy reconcile
./knowl validate
```

The command invokes the configured maintainer once through a bounded structured
hierarchy plan, owns only `wiki/index.md` and `wiki/catalogs/**/index.md`, and
prints the durable operation ID, status, changed flag, generation, and affected
files as JSON. It does not start HTTP, scheduled work, or `sync.on_start`.
Re-running a converged hierarchy reports `"changed":false` without changing
canonical bytes. Ordinary pages and immutable `raw/` revisions are never
hierarchy targets.

The generic hierarchy contract is subject-first: document kind and technology
are supporting signals, broad heterogeneous subjects are recursively decomposed,
and secondary catalog membership is used sparingly for genuinely cross-cutting
pages. The maintainer is asked to retain suitable current paths and memberships
for stability, but semantic quality remains provider-dependent. Its single call
receives bounded page metadata and excerpts plus the schema digest, not schema
content, raw sources, provenance, or source-native paths. Application validation
rejects empty generated non-root catalogs and incomplete or unsafe graphs before
staging; an empty root is valid only for an empty wiki.
This contract uses planner identity `hierarchy-v3`; existing catalogs change only
after an explicit reconcile under that identity.

Legacy workspaces are never rewritten implicitly. Back them up and run:

```bash
./knowl migrate okf-v0.2
./knowl validate
```

The migration is journaled, interruption-safe, idempotent, preserves legacy
content and logs, and rebuilds the configured SQL projection. Attested
Computation fields are stored and returned only as inert metadata; Knowl never
executes computations, executors, attesters, or referenced resources.

Operational-store migration to generic source/hierarchy operations is additive
for SQLite and PostgreSQL. Older binaries remain safe only while no hierarchy
operation rows have been created; after the first reconcile, roll back by
restoring the pre-upgrade operational database or keep the newer binary. The
Markdown wiki remains canonical and portable in either case.

## Public packages

- `pkg/knowl/types` — transport-neutral domain types
- `pkg/knowl` — plain-Go host/runtime composition
- `pkg/knowlfx` — Fx lifecycle wrapper over `pkg/knowl`
- `pkg/knowl/mcp` — the three-tool MCP adapter

## Where to look next

- sidecar/service runbook: [docs/sidecar.md](docs/sidecar.md)
- v0.3.1 legacy-provenance patch and upgrade notes: [docs/releases/v0.3.1.md](docs/releases/v0.3.1.md)
- v0.3.0 semantic-wiki release and upgrade notes: [docs/releases/v0.3.0.md](docs/releases/v0.3.0.md)
- v0.2.0 multi-source release and migration notes: [docs/releases/v0.2.0.md](docs/releases/v0.2.0.md)
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
