# Knowl documentation

Choose how to run Knowl. Both paths produce a wiki from your sources and let you
retrieve evidence from it:

| Local | Service |
| --- | --- |
| [Local quickstart](local.md): a project-local wiki maintained by Codex through ACP. | [Service quickstart](service.md): a persistent MCP/HTTP service with its own storage. |

## Configure your workflow

- [Bring your own agent](agents.md): Codex and custom ACP stdio maintainers.
- [Codex plugin](local-codex.md): skills and consumer MCP integration.
- [Search](search.md): lexical search, optional hybrid search, external embedding APIs and a local TEI server.
- [Web UI](web-ui.md): browse pages, saved sources, search and processing history.
- [HTTP facade access control](access-control.md): independent API, MCP and Web authentication, read-only defaults, and proxy deployments.
- [Operations](operations.md): shared configuration, sources, authentication, diagnostics and recovery.
- [Workspace and export](workspace.md): canonical Markdown, immutable raw sources and OKF portability.

## See it working

[Knowl's own documentation wiki](examples/own-docs-wiki.md) is a working example
in this repository. Compare [source documentation](.) with the
[generated wiki](../knowledge/wiki/index.md), and inspect its saved provenance.

## Reference

- [Product design](design.md): architecture, contracts and invariants.
- [Testing and measurements](testing.md): verification and retrieval evaluations.
- [SearchTome evaluation](testing/searchtome-results.md): lexical and hybrid retrieval results, methodology and reproduction runbook.
- [HTTP API](../api/openapi/knowl.yaml): the public OpenAPI contract.
- [Go application integration](operations.md#go-application-integration).
- [Release history](releases/): version-specific changes and historical instructions.
- [Publishing](publishing.md): release gates and one-time npm trusted-publisher setup.

Current guides describe the checked-in source. A recipe that uses a published
release names that version explicitly; historical release recipes do not imply
that older binaries include newer features.
