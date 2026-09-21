---
name: run
description: Run one bounded project-local Knowl maintenance cycle and report its results and owned file changes. Use for all configured sources or one explicit source, not for starting a daemon.
---

# Knowl run

Operate from the active project's root directory. Use exactly one compatible CLI form for the entire workflow:

1. Execute `knowl version --json` if `knowl` is available.
2. Use local `knowl` only when the JSON says `{"version":"0.6.0","tag":"v0.6.0","release":true}`.
3. Otherwise use the argv prefix `npx`, `--yes`, `@baldaworks/knowl@0.6.0`.

Do not mix the two forms during one run and do not use `latest`.

## Scope and safety

Run all enabled sources unless the user explicitly supplies one source ID. A supplied ID must match `^[a-z0-9][a-z0-9._-]{0,63}$`; pass it as its own argv value after `--source`, never through shell interpolation.

Before changing anything, identify the configured Knowl workspace and record Git status for only `.config/knowl` and the Knowl-owned workspace paths. If the workspace is outside the repository, record an equivalent bounded file listing. Never stash, reset, checkout, clean, stage, commit, or rewrite unrelated user changes.

This is a one-shot local workflow. Never invoke `start`, `mcp`, an HTTP URL, or a network listener.

## Workflow

Using the selected CLI prefix:

1. Run `validate`. Stop before maintenance if it fails.
2. Run `run` for all sources, or run `run`, `--source`, `<validated-source-id>` for the explicitly selected source. Preserve stdout separately from diagnostics and retain it even when the process exits nonzero.
3. Parse stdout as the `run` JSON result. Capture:
   - each `sources` entry's `source_id`, `run`, `changed`, `failure_class`, and diagnostics;
   - `operations.completed`, `operations.retried`, `operations.failed`, and `operations.total`;
   - `hierarchy.status`, `hierarchy.changed`, `hierarchy.generation`, and `hierarchy.files` when present.
4. Run `validate` again, including after a failed maintenance command when it is safe to do so.
5. Record the same scoped post-run status and report only changes to Knowl-owned paths. Leave every unrelated dirty file untouched.

Treat the workflow as failed if the maintenance command exits nonzero, its stdout is not valid result JSON, `operations.failed` is greater than zero, or the final validation fails. Partial JSON remains useful evidence: report its completed/retried/failed counts and source or hierarchy outcomes before explaining the failure. Do not turn a nonzero exit into success merely because JSON was emitted.

On success, summarize source changes, all operation totals, hierarchy generation/files when present, and the scoped file delta. An absent `hierarchy` field means hierarchy reconciliation was unavailable or not produced; do not invent an outcome.
