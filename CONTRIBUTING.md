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

By submitting a contribution, you license it under the repository's MIT
License.
