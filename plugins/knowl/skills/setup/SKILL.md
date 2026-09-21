---
name: setup
description: Set up or repair project-local Knowl and its Codex plugin from the pinned npm release. Use for local Codex projects, not hosted or remote environments.
---

# Knowl setup

Run setup from the active project's root directory. The supported release is exactly `0.6.0`.

Before running it, tell the user that setup may:

- download the pinned npm package and the pinned Git marketplace;
- create only missing Knowl project files under `.config/knowl`, `wiki`, `raw`, and `.knowl`;
- update the user's Codex marketplace and plugin configuration.

Existing project configuration and knowledge files must remain unchanged. Do not start `knowl start`, configure an HTTP endpoint or token, or register MCP separately; the installed plugin already supplies project-scoped MCP stdio configuration.

## Workflow

1. Run this exact command directly, without a shell-composed variant:

   ```text
   npx --yes @baldaworks/knowl@0.6.0 setup codex
   ```

2. Parse its JSON result. Require `version` to be `0.6.0`; report the `project`, `marketplace`, `plugin`, and `restart_required` fields. Stop on a nonzero exit or malformed/mismatched result.
3. Validate the resulting project with the same pinned executable:

   ```text
   npx --yes @baldaworks/knowl@0.6.0 validate
   ```

4. If either integration field changed or `restart_required` is true, tell the user to start a new Codex thread in this project so Codex loads the installed plugin and MCP server.

Repeating this workflow is the repair path for an already-installed compatible plugin and should be a no-op when everything is current.

If setup reports a marketplace or plugin conflict, explain what conflicts and ask for explicit authorization before rerunning the exact setup command with `--replace`. Do not remove or replace user-level Codex state without that answer. `--replace` never authorizes rewriting project files.

For a machine where the plugin is not installed yet, the bootstrap entrypoint is the same pinned `npx ... setup codex` command run directly by the user or agent; afterward, continue in a new Codex thread. Do not invent a separate agent-setup workflow.
