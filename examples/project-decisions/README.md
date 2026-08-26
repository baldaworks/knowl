# Project-decisions host example

This example shows a Balda, Norma, or equivalent host choosing durable project
events and sending their immutable revisions to a running Knowl sidecar. The
host owns event selection and the final answer. Knowl maintains the Markdown
knowledge artifact and returns bounded, untrusted evidence with provenance.

The checked-in corpus contains, in order:

1. an ADR selecting Badger for session memory;
2. a crash-recovery investigation;
3. a new ADR superseding Badger with SQLite while retaining history;
4. a linked session-recovery runbook.

The Go host uses only `knowl_ingest`, `knowl_operation`, and `knowl_retrieve`.
It never reads or writes Knowl's workspace, SQL store, or projections.

## Run it

Start a configured sidecar whose maintainer provider can update project
knowledge, then run from the repository root:

```bash
go run ./examples/project-decisions
```

The default MCP endpoint is `http://127.0.0.1:8080/mcp`. Override it when the
sidecar is elsewhere:

```bash
KNOWL_MCP_ENDPOINT=http://127.0.0.1:9090/mcp \
  go run ./examples/project-decisions
```

For an authenticated sidecar, pass the same operator token used by Knowl:

```bash
KNOWL_OPERATOR_TOKEN=replace-with-the-configured-secret \
  go run ./examples/project-decisions
```

The program submits each source, polls its durable operation to a terminal
state, asks why Badger was selected and what replaced it, prints the returned
untrusted evidence, `source_refs`, and resolved `source_documents`, and only
then produces a line labeled `Host answer`. Knowl itself does not generate that
final answer.

Re-running the command is safe: each source has a stable origin and revision.
Change a revision only when the corresponding source content changes. In a
real host, GitHub, Jira, chat, or story-completion logic stays outside Knowl and
decides which accepted artifact is durable enough to submit.

Run the deterministic smoke test with:

```bash
go test ./examples/project-decisions
```

The test uses a temporary real Knowl Host and Streamable MCP connection with a
fixed maintainer, so it needs no external model, credentials, Docker, or
network service.
