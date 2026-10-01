# Knowl

[![test](https://github.com/baldaworks/knowl/actions/workflows/test.yml/badge.svg)](https://github.com/baldaworks/knowl/actions/workflows/test.yml)
[![lint](https://github.com/baldaworks/knowl/actions/workflows/lint.yml/badge.svg)](https://github.com/baldaworks/knowl/actions/workflows/lint.yml)
[![release](https://img.shields.io/github/v/release/baldaworks/knowl)](https://github.com/baldaworks/knowl/releases/latest)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Your project knowledge, maintained as a wiki.**

Knowl is a self-hosted LLM wiki for your projects. It turns source documents
into connected Markdown pages and gives AI agents grounded evidence from that
wiki. Knowledge remains available across tasks, with references back to the
sources that support it.

Run Knowl with your own storage and maintainer provider. The wiki uses
[Open Knowledge Format (OKF) v0.2](docs/workspace.md), so you can inspect it,
review it in Git, and export it as a portable bundle.

[Quickstart](#minimal-sidecar-quickstart) ·
[Use with Codex](#local-codex-plugin) ·
[Connect an agent](#connect-an-agent) ·
[Documentation](#documentation-by-goal)

## What You Get

| Need | What Knowl provides |
| --- | --- |
| Keep project knowledge across tasks | An LLM-maintained wiki built from durable source revisions |
| Understand where a result came from | Bounded evidence linked to its supporting sources |
| Own and inspect your knowledge | Plain Markdown in an OKF-compatible workspace |
| Choose where it runs | Local Codex, a self-hosted MCP/HTTP service, or an embedded Go runtime |

## From Sources to a Wiki

Connect project sources or start from an existing Markdown wiki, Obsidian vault,
or OKF bundle. Knowl preserves accepted source revisions, and the configured
LLM maintainer proposes semantic updates to the wiki. Updates pass validation
before they become part of the knowledge base.

Your agent retrieves relevant evidence and uses it to answer or act. You can
also read the wiki directly, inspect its source references, or export it for
publishing. See the [source-to-wiki example](examples/source-to-wiki/README.md)
for a complete walkthrough.

## Minimal Sidecar Quickstart

The quickstart runs the published
[v0.5.0](https://github.com/baldaworks/knowl/releases/tag/v0.5.0) image with a
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

### Local Codex plugin

Use Knowl from a local Codex project with setup and maintenance skills plus
MCP tools. For the v0.6.0 npm release, setup is:

```bash
npx --yes @baldaworks/knowl@0.6.0 setup codex
```

The npm instructions require that release to be published.
After setup, start a new Codex thread and use `$knowl:run` for one bounded
wiki maintenance cycle.
See the [local Codex guide](docs/local-codex.md) for requirements, provider
configuration, offline use, and supported scope.

### MCP and HTTP

Connect other agents to the self-hosted service over MCP. Knowl exposes three
tools: `knowl_retrieve` for evidence, `knowl_ingest` for durable inputs, and
`knowl_operation` for operation status.

The same capabilities are available through HTTP. See
[sidecar deployment](docs/sidecar.md#connect-an-mcp-client),
[service operations](docs/operations.md#http-contract), and the
[OpenAPI contract](api/openapi/knowl.yaml).

### Go applications

Embed Knowl through `pkg/knowl`, or use `pkg/knowlfx` for Fx lifecycle
integration. See the [product design](docs/design.md) for runtime composition
and ownership boundaries.

## Keep Your Knowledge Portable

The canonical wiki lives in `wiki/` as an OKF v0.2 bundle. Accepted source
revisions are preserved separately in `raw/`, so semantic pages remain connected
to their evidence. The default local operational store uses SQLite.

Export the wiki as a standalone bundle, or add an `llms.txt` navigation file for
agents and publication. See [workspace and export](docs/workspace.md) for the
layout and commands.

## Documentation By Goal

| Goal | Start here |
| --- | --- |
| Set up local Codex | [Local Codex guide](docs/local-codex.md) |
| Deploy an MCP/HTTP service | [Sidecar deployment](docs/sidecar.md) |
| Configure providers, sources, and recovery | [Operations guide](docs/operations.md) |
| Understand storage and source provenance | [Workspace guide](docs/workspace.md) |
| Export the wiki for publishing or agents | [OKF and llms.txt export](docs/workspace.md#export-for-publication) |
| Embed Knowl in an application | [Product design](docs/design.md) |
| Integrate over HTTP | [OpenAPI contract](api/openapi/knowl.yaml) |
| See source documents become a wiki | [Source-to-wiki showcase](examples/source-to-wiki/README.md) |
| Review the container quickstart release | [v0.5.0 release notes](docs/releases/v0.5.0.md) |
| Review the npm/plugin release | [v0.6.0 release notes](docs/releases/v0.6.0.md) |

## License

Knowl is released under the [MIT License](LICENSE).
