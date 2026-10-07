# Knowl maintains its own documentation wiki

This repository is a working Knowl example. Its [documentation](../README.md)
is the source corpus; the [published wiki](../../knowledge/wiki/index.md) is the
maintained knowledge artifact. The agent synthesizes connected pages from the
docs rather than copying their directory layout. Accepted source revisions stay
under `knowledge/raw/`, separate from the generated pages.

## Configuration

The checked-in [configuration](../../.config/knowl/config.yaml) selects:

| Responsibility | Repository configuration |
| --- | --- |
| Source | `knowl-docs`, filesystem root `docs`, include `**/*.md` |
| Maintainer | `antigravity_acp`, model `gemini-3.8-flash-medium` |
| Published wiki | `knowledge/wiki/` |
| Accepted revisions | `knowledge/raw/` |
| Operational state | SQLite under `knowledge/.knowl/` |
| Search | Hybrid with local E5-base at `127.0.0.1:8091`, explicit lexical fallback |

This is the repository's own setup. The [local quickstart](../local.md) shows
Codex, and [agent configuration](../agents.md) explains custom ACP stdio agents.
Maintainer choice and [embedding configuration](../search.md) are independent.

## Generate and inspect

From the repository root, you need the Go version in `go.mod`, Task, Node.js/npm,
and an authenticated Antigravity runtime with access to the configured model.
The [Taskfile](../../Taskfile.yml) resolves the pinned ACP runner/bridge; a cold
run needs network access to download it. The configured agent receives the
selected documentation as source material. Run the local embedding server for
hybrid retrieval; with the checked-in fallback policy, an unavailable server
permits lexical retrieval instead.

```bash
task wiki:generate
```

This resolves `antigravity-acp`, runs `knowl run --source knowl-docs`, and validates
the workspace. The run synchronizes source revisions, drains ready maintenance,
and reconciles the wiki's semantic navigation. It preserves existing accepted
raw history. It does not push Git changes.

Inspect processing and retrieve from the resulting wiki:

```bash
go run ./cmd/knowl source status knowl-docs
go run ./cmd/knowl retrieve 'How do I configure an ACP maintainer?'
task wiki:validate
git diff --stat -- knowledge
git diff -- knowledge/wiki
```

Check maintenance counts and operation samples as well as the scan result.
A successful source scan or structural validation alone does not establish that
all agent updates completed. Follow [source recovery](../operations.md#recovering-failed-source-maintenance)
for failures or deferred retries, and inspect the retrieval mode to distinguish
hybrid results from degraded lexical fallback.

Review generated facts against their cited accepted revisions. Each factual
page carries `knowl.source_refs`; the [Web UI](../web-ui.md) exposes them through
**Page sources** and **Read saved source**. The wiki and saved source revisions
can differ from the latest docs until maintenance completes. New Markdown files
under `docs/` also enter this corpus; keep credentials and local overrides out of
it.

Commit and publish reviewed wiki/raw changes through the repository's normal Git
workflow. Preserve immutable raw history when correcting generated pages or
rolling back a change. See [workspace semantics](../workspace.md) for storage,
provenance and export.
