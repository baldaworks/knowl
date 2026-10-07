# Bring your own agent

Knowl uses your selected agent to read sources and propose wiki updates. It
validates those proposals before publishing them. The agent connects through
ACP (Agent Client Protocol) over stdio; select it with `knowl.provider` from the
`runtime.providers` registry.

This connection has a different role from MCP: ACP connects Knowl to its wiki
maintainer, while MCP connects a consumer agent to Knowl's retrieval, ingest and
operation tools. Codex can serve either role, or both.

## Codex example

Install and authenticate the Codex CLI and make Node.js/npm available to the
Knowl process. The pinned runtime launches its Codex ACP bridge through `npx`.
The [local quickstart](local.md) uses:

```yaml
runtime:
  providers:
    codex:
      type: codex_acp
      codex_acp:
        bridge_version: "1.7.3"
knowl:
  provider: codex
```

This is a fragment to merge into your Knowl configuration. `model`,
`reasoning_effort` and `mode` can select capabilities supported by your agent.
Omit optional selections to use the agent's defaults. A configured model still
needs to be available to the authenticated account.

The [Codex plugin guide](local-codex.md) covers consumer skills and MCP setup.
Installing that plugin does not select Codex as the wiki maintainer; the
`knowl.provider` setting does.

## Custom ACP agent over stdio

Use `generic_acp` for your own ACP executable:

```yaml
runtime:
  providers:
    my-agent:
      type: generic_acp
      generic_acp:
        cmd: ["/absolute/path/to/your-acp-agent"]
        extra_args: ["--stdio"]
knowl:
  provider: my-agent
```

Replace the executable and arguments with the real agent's ACP launch command.
`cmd` is an argv prefix, and `extra_args` appends arguments; shell command syntax
is not interpreted. `--stdio` is illustrative: only use it if your agent accepts
that flag. The process must speak ACP on stdin/stdout. Install and authenticate
it in the environment where Knowl runs, including inside the container when
using a container deployment.

Knowl supports custom ACP agents through this generic protocol adapter. Each
agent must implement the ACP session behavior needed by the runtime and return
plans satisfying Knowl's structured output contract. Protocol compatibility
does not mean every third-party agent has been tested. Validate your setup with
one source and inspect its operation before processing a large collection.

## Other providers and failures

The provider registry also supports named ACP adapters and hosted model APIs.
The [service example](service.md) uses the existing OpenAI provider so the base
container needs no separately installed ACP executable. The repository's
[own-docs example](examples/own-docs-wiki.md) uses Antigravity through ACP.

Agent failures appear in maintenance operation status. A successful source sync
only accepts a source revision; it does not prove the agent completed the wiki
update. See [processing and recovery](operations.md#recovering-failed-source-maintenance).

Embedding models are configured independently under `knowl.embeddings`; they
support [hybrid retrieval](search.md) and do not replace the maintainer.
