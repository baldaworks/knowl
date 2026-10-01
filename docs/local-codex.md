# Use Knowl with local Codex

The local plugin connects your project's Knowl wiki to Codex through MCP stdio.
It provides setup and bounded maintenance skills without a separate service.

## Install

These instructions require the v0.6.0 npm release to be published. See the
[release notes](releases/v0.6.0.md) for availability and supported scope.
The primary package is `knowl`; `@baldaworks/knowl` is an equivalent alias.
Both use the same native binaries and exact release version.

You need Node.js with npm/npx and the Codex CLI. For maintenance, the default
project configuration also requires an installed and authenticated
`opencode acp` runtime. Run this once from the project root:

```bash
npx --yes knowl@0.6.0 setup codex
```

Setup creates only missing project state (`.config/knowl`, `schema.md`, `raw/`,
`wiki/`, and `.knowl/`), validates it, and installs the release-matched Knowl
marketplace and plugin. Existing project files are preserved. A conflicting
Knowl-managed marketplace or plugin is replaced only after explicit approval
and a rerun with `--replace`.

## Use the wiki

Start a new Codex thread in the project after setup. The plugin provides exactly
two workflow skills:

- `$knowl:setup` repeats or repairs the same pinned setup workflow;
- `$knowl:run` validates, performs one bounded `knowl run`, validates again,
  and reports structured results and changes under Knowl-owned paths.

The run skill uses a local `knowl` only when `knowl version --json` identifies
the exact `0.6.0` release; otherwise it uses the pinned
`npx --yes @baldaworks/knowl@0.6.0` launcher for the whole workflow. Maintenance
also requires the configured provider; the default local configuration expects
an installed and authenticated `opencode acp` runtime.

## MCP connection

Plugin installation also registers the project-scoped MCP server. Codex starts
`npx --yes @baldaworks/knowl@0.6.0 mcp stdio` with the active project as its
working directory and owns that child process. Do not run a daemon, configure
`codex mcp add`, set an operator token, or allocate a port for this path.

## Platforms and offline use

The npm release contains native packages for macOS x64/arm64, Linux x64/arm64,
and Windows x64. Setup and the first MCP launch can download the exact npm
package; setup also fetches the pinned Git marketplace. Prewarm both while
online when later work must be offline. The sparse marketplace checkout limits
the materialized working-tree paths to plugin assets, but it does not promise a
metadata-only Git transfer or eliminate all repository metadata fetches.

## Supported scope

This plugin workflow is supported for local Codex projects on a local
filesystem. Hosted Codex, remote MCP hosting, shared/network filesystems, and
secondary workspace folders are outside the verified contract. Separate local
projects resolve separate state. Multiple local Codex threads in one project
share the durable store and coordinated workspace, while unrelated user changes
remain outside Knowl's write scope.

For provider and source configuration, see [operations](operations.md). For
wiki layout, provenance, and OKF export, see [workspace semantics](workspace.md).
