# Contributing to Knowl

For a non-trivial behavior change, open an issue first so the intended public
contract is clear. Keep each pull request small and focused.

Use the Go version declared in `go.mod`. Before opening a pull request, run:

```bash
go test ./...
go tool golangci-lint run ./...
```

Run `go mod verify` after dependency changes. PostgreSQL container coverage is
kept behind the `integration` build tag; do not add it to the default fast test
lane.

Use Conventional Commits in imperative mood. Update OpenAPI, generated HTTP
bindings, and public documentation together whenever a public contract changes.
Do not commit `.config/knowl/*.local.yaml`; keep `.beads/` in the repository.

## Maintaining Knowl's project wiki

This repository uses Knowl itself to maintain the checked-in
[project wiki](knowledge/wiki/index.md). The `knowl-docs` filesystem source
reads non-hidden Markdown below `docs/` through the `docs/**/*.md` include in
[.config/knowl/config.yaml](.config/knowl/config.yaml); generated output cannot
feed back into that source.

The operator-owned [self-wiki policy](knowledge/schema.md) guides taxonomy and
synthesis as untrusted Markdown. Knowl's Go validation still enforces workspace
safety, OKF, provenance, and link invariants.

From the repository root, refresh and validate the wiki with:

```bash
task wiki:generate
task wiki:validate
```

Generation requires the Go version declared in `go.mod`, Task, Node.js/npm for
the pinned `acprun` invocation, and a usable Antigravity ACP session. The task
resolves the pinned ACP binary, runs only the `knowl-docs` source, reconciles
the hierarchy, and validates the result. `wiki:validate` is local-only and does
not invoke the provider.

Commit `knowledge/schema.md`, `knowledge/raw/**`, and `knowledge/wiki/**`. Treat
`knowledge/.knowl/**` as rebuildable local operational state. Generated prose
and organization are model-dependent, so regeneration is an explicit
maintainer action rather than a CI requirement.

Before committing a refresh, review the generated diff, its
`knowl.source_refs`, and the append-only `knowledge/wiki/log.md`. Check factual
changes against the cited raw revision rather than relying on syntactic validity
alone.

By submitting a contribution, you license it under the repository's MIT
License.
