# AGENTS Guidelines

## Branching & Commits

- Branch from `main` for new work.
- Use Conventional Commits in imperative mood.

## Preferred Workflow

- Use the Go version declared in `go.mod`.
- Run `go test ./...` before pushing.
- Run `go tool golangci-lint run ./...` before pushing.
- Run `go mod verify` after dependency changes.
- Close a Prism Story only after all required GitHub checks are green and all changes belonging to the Story have been merged.

## Repository Conventions

- Keep `.beads/` in the repository; do not delete or blanket-ignore Beads state.
- Keep PostgreSQL container coverage behind the `integration` build tag.
- Treat `.config/knowl/*.local.yaml` as local-only config, not committed source.

## Code Style

- Keep public docs and config examples aligned with the checked-in Go types.
- Prefer small, reviewable changes over broad refactors.
- Preserve deterministic tests and bounded local defaults.
