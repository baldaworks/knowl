# Knowl

[![test](https://github.com/baldaworks/knowl/actions/workflows/test.yml/badge.svg)](https://github.com/baldaworks/knowl/actions/workflows/test.yml)
[![lint](https://github.com/baldaworks/knowl/actions/workflows/lint.yml/badge.svg)](https://github.com/baldaworks/knowl/actions/workflows/lint.yml)
[![release](https://img.shields.io/github/v/release/baldaworks/knowl)](https://github.com/baldaworks/knowl/releases/latest)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Durable project knowledge for agents.**

Knowl is a self-hosted knowledge sidecar for agentic applications. It turns
durable sources into an inspectable Markdown knowledge base and returns
bounded, provenance-backed evidence.

[Quickstart](#minimal-sidecar-quickstart) ·
[Connect an agent](#connect-an-agent) ·
[Documentation](#documentation-by-goal) ·
[GitHub](https://github.com/baldaworks/knowl)

## What Users Get

| Capability | Result |
| --- | --- |
| Durable ingestion | Accepted source revisions survive restarts and remain traceable |
| Inspectable knowledge | Semantic pages live in a portable, Git-reviewable Markdown workspace |
| Grounded retrieval | Every response is bounded evidence with source provenance |
| Agent integration | The same contract is available over MCP, HTTP, or embedded Go |

## Why Knowl

- **Built for durable knowledge:** keep accepted project knowledge beyond one
  chat or agent run.
- **Human-inspectable:** review the generated Markdown instead of hiding
  knowledge in an opaque memory store.
- **Clear ownership:** the host selects durable inputs and writes the final
  answer; Knowl maintains knowledge and retrieves evidence.
- **Self-hosted:** run it beside an agent with your own storage and maintainer
  provider.
- **Recoverable:** durable operations and local state resume safely after
  process restarts.

Knowl is not session memory, a workflow orchestrator, or the primary
final-answer generator. Knowl does not answer the user itself.

## Minimal Sidecar Quickstart

The quickstart runs the published
[v0.3.1](https://github.com/baldaworks/knowl/releases/tag/v0.3.1) image with a
checked-in example source. It requires Git, Docker Compose, `curl`, an OpenAI
API key, and a model available to that key.

```bash
git clone https://github.com/baldaworks/knowl.git
cd knowl

export OPENAI_API_KEY='your-api-key'
export OPENAI_MODEL='a-model-available-to-your-account'
export KNOWL_OPERATOR_TOKEN='replace-with-a-local-secret'

docker compose -f deploy/sidecar/quickstart.compose.yaml up -d
curl -sS http://127.0.0.1:8080/readyz
```

The configured source synchronizes on startup. Check maintenance status:

```bash
docker compose -f deploy/sidecar/quickstart.compose.yaml \
  exec knowl knowl --config-dir /etc source status engineering
```

Then retrieve grounded evidence:

```bash
curl -sS --get \
  -H "Authorization: Bearer ${KNOWL_OPERATOR_TOKEN}" \
  --data-urlencode 'query=Engineering shared page' \
  http://127.0.0.1:8080/v1/retrieve
```

A successful response contains a non-empty `evidence` array with source
provenance. `/readyz` confirms that the service and storage are ready; use
`source status` to confirm that model-backed maintenance completed.

Stop the example without deleting its persistent volume:

```bash
docker compose -f deploy/sidecar/quickstart.compose.yaml down
```

The quickstart uses the hosted `openai` maintainer provider. Other
configurations may use `opencode_acp`, which requires `opencode acp` on `PATH`
and an authenticated OpenCode session. See
[configuration and operations](docs/operations.md) for provider and source
settings.

## Connect an Agent

MCP Streamable HTTP is the primary agent-facing interface. Adapt these fields
to your MCP client:

```json
{
  "transport": "streamable_http",
  "url": "http://127.0.0.1:8080/mcp",
  "headers": {
    "Authorization": "Bearer <operator-token>"
  }
}
```

Knowl exposes three MCP tools:

- `knowl_retrieve` returns bounded evidence;
- `knowl_ingest` submits durable content or a source reference;
- `knowl_operation` reports durable operation status.

The equivalent HTTP endpoints are `GET /v1/retrieve`, `POST /v1/ingest`, and
`GET /v1/operations/{operation_id}`. See the
[OpenAPI contract](api/openapi/knowl.yaml) for request and response schemas.

## How It Works

```text
sources → Knowl → grounded evidence → host agent → final answer
```

| Component | Owns |
| --- | --- |
| Host agent or application | Selecting durable inputs, orchestrating tools, and generating the final answer |
| Knowl | Preserving source revisions, maintaining Markdown knowledge, and retrieving evidence |
| Maintainer provider | Proposing semantic updates through Knowl's validated write path |

Knowl stores immutable accepted source revisions under `raw/` and semantic
knowledge under `wiki/`. Source documents are never copied into `wiki/`.
Initial bootstrap and automatic `on_start` synchronization are both optional.
The workspace remains inspectable and portable, with SQLite at
`.knowl/knowl.sqlite` providing the default local operational store:

```text
workspace/
├── schema.md
├── raw/
├── wiki/
│   ├── index.md
│   └── ... semantic pages
└── .knowl/
    └── knowl.sqlite
```

See the [source-to-wiki showcase](examples/source-to-wiki/README.md) for a
complete checked-in example.

## Run Knowl Your Way

### Sidecar

Use the published container for a standalone MCP/HTTP service. Persist
`/var/lib/knowl`, keep source mounts read-only, and pin an immutable image
digest in production. The [sidecar guide](docs/sidecar.md) covers deployment,
storage, authentication, and health checks.

### Go library

- `pkg/knowl` provides plain-Go runtime composition;
- `pkg/knowlfx` adds Fx lifecycle integration;
- `pkg/knowl/mcp` provides the MCP adapter;
- `pkg/knowl/types` contains transport-neutral domain types.

Embedding changes composition, not the business contract. See the
[product design](docs/design.md) for architecture and ownership boundaries.

## Documentation By Goal

| Goal | Start here |
| --- | --- |
| Deploy the sidecar | [Sidecar deployment](docs/sidecar.md) |
| Configure providers, sources, and recovery | [Operations guide](docs/operations.md) |
| Understand workspace and provenance semantics | [Workspace guide](docs/workspace.md) |
| Understand the architecture | [Product design](docs/design.md) |
| Integrate over HTTP | [OpenAPI contract](api/openapi/knowl.yaml) |
| See source documents become a wiki | [Source-to-wiki showcase](examples/source-to-wiki/README.md) |
| Review the latest release | [v0.3.1 release notes](docs/releases/v0.3.1.md) |

## Contributing

Contributions are welcome. Start with [CONTRIBUTING.md](CONTRIBUTING.md) for
local setup, verification commands, and repository conventions.

## License

Knowl is released under the [MIT License](LICENSE).
