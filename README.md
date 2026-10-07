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

Knowl builds on Andrej Karpathy's [LLM Wiki pattern](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f):
an agent reads sources and maintains connected knowledge over time. Bring your
own ACP-compatible agent to maintain the wiki. Your knowledge stays in portable,
[Open Knowledge Format (OKF)](docs/workspace.md) Markdown under your control.

[Get started](#get-started) ·
[Browse the wiki](docs/web-ui.md) ·
[See Knowl's own wiki](knowledge/wiki/index.md) ·
[Documentation](docs/README.md)

## What You Get

| Need | What Knowl provides |
| --- | --- |
| Keep project knowledge across tasks | An LLM-maintained wiki built from durable source revisions |
| Understand where a result came from | Bounded evidence linked to its supporting sources |
| Own and inspect your knowledge | Plain Markdown in an OKF-compatible workspace |
| Read the wiki in your browser | An optional read-only console for pages, saved sources, search, and processing history |
| Find related evidence across different wording | Optional hybrid search through your own embedding API or local CPU server |
| Choose where it runs | Local Codex, a self-hosted MCP/HTTP service, or an embedded Go runtime |

## From Sources to a Wiki

Connect project sources or start from an existing Markdown wiki, Obsidian vault,
or OKF bundle. Knowl preserves accepted source revisions, and the configured
LLM maintainer proposes semantic updates to the wiki. Updates pass validation
before they become part of the knowledge base.

A URL submission stores a reference. To use the page's contents as a source,
submit its text. See the [ingest examples](docs/operations.md#http-examples).

Your agent retrieves relevant evidence and uses it to answer or act. You can
also read the wiki directly, inspect its source references, or export it for
publishing. See the [source-to-wiki example](examples/source-to-wiki/README.md)
for a complete walkthrough.

## Get Started

| Local | Service |
| --- | --- |
| Keep a wiki alongside your project and use a local ACP agent to maintain it. | Run a persistent Knowl service and connect clients over MCP or HTTP. |
| [Local quickstart with Codex](docs/local.md) | [Service quickstart](docs/service.md) |

Both guides take you from a source document to a published wiki page and a
retrieval result. Choose your maintainer in the [agent guide](docs/agents.md),
including a custom agent through `generic_acp` over stdio.

<a id="minimal-sidecar-quickstart"></a>
The container quickstart is now in the [service guide](docs/service.md).

## Knowl Uses Knowl

Knowl maintains a wiki of its own documentation. Compare the [source docs](docs/)
with the [generated wiki](knowledge/wiki/index.md) to see source material become
connected knowledge. The [walkthrough](docs/examples/own-docs-wiki.md) explains
the configured ACP agent, generation commands and saved source provenance.

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
[service connection guide](docs/service.md#connect-an-mcp-client),
[service operations](docs/operations.md#http-contract), and the
[OpenAPI contract](api/openapi/knowl.yaml).

Knowl can request one corrected plan when generated output fails validation.
Inspect the result through operation status, or
[disable correction](docs/operations.md#bounded-output-correction) in your config.

### Go applications

Embed Knowl through `pkg/knowl`, or use `pkg/knowlfx` for Fx lifecycle
integration. See the [product design](docs/design.md) for runtime composition
and ownership boundaries.

## Read Your Wiki in a Browser

Enable the optional [web UI](docs/web-ui.md) to browse published pages, inspect
their saved source revisions, retrieve evidence, and follow maintenance status.
It runs inside the Go service with locally bundled assets and is disabled by
default. Connect with an operator token; use HTTPS when exposing the service
beyond loopback.

## Search Across Different Wording

Enable optional embeddings to retrieve related evidence across paraphrases and
word forms. Knowl combines semantic and keyword search while keeping the
original page text and source references in its results. The semantic index
covers the full text of each published page within the configured projection
capacity; a dense-only match shows the matching passage as its excerpt.

Use your own compatible embedding API or the
[local CPU embedding server](docs/search.md#local-embedding-server).
Embeddings are off by default. Choose explicit keyword fallback or strict
semantic availability in the [search guide](docs/search.md).

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
| Run a local wiki with Codex | [Local quickstart](docs/local.md) |
| Run Knowl as an MCP/HTTP service | [Service quickstart](docs/service.md) |
| Bring your own ACP agent | [Agent configuration](docs/agents.md) |
| Use the Codex plugin | [Codex integration](docs/local-codex.md) |
| Browse the wiki | [Web UI](docs/web-ui.md) |
| Configure lexical or hybrid search | [Search](docs/search.md) |
| See Knowl maintain its own wiki | [Own-docs walkthrough](docs/examples/own-docs-wiki.md) |
| Operate and recover Knowl | [Operations](docs/operations.md) |
| Understand storage and export | [Workspace and OKF](docs/workspace.md) |
| Embed Knowl in Go | [Go application integration](docs/operations.md#go-application-integration) |

See the [documentation index](docs/README.md) for architecture, API contracts,
quality measurements and release history.

## License

Knowl is released under the [MIT License](LICENSE).
