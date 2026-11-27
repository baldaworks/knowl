# Multiple knowledge scopes

One Knowl host can serve several isolated knowledge bases, called **scopes**.
Each scope owns its own `schema.md`, immutable `raw/` history and portable
`wiki/` OKF bundle. The operational database, provider capacity and worker
limit remain shared by the host.

Scope selection always names exactly one configured public scope. Omitting the
selector uses `default_scope`. Knowl does not provide `scope=*`, scope arrays,
cross-scope links or implicit federated retrieval.

## Configure two scopes

Declare sources inside their owning scope. The same source ID may be used in
two scopes; the resulting source histories and operations remain independent.

```yaml
runtime:
  providers:
    openai:
      type: openai
      openai:
        api_key: ${OPENAI_API_KEY}
        model: ${OPENAI_MODEL}

knowl:
  provider: openai
  workspace:
    path: knowledge
  default_scope: default
  scopes:
    default:
      ref: existing-project
      sources:
        - id: handbook
          type: filesystem
          filesystem:
            root: sources/product
            include: ["**/*.md"]
            flavor: markdown
    infrastructure:
      sources:
        - id: handbook
          type: filesystem
          filesystem:
            root: sources/infrastructure
            include: ["**/*.md"]
            flavor: markdown
  storage:
    type: sqlite
    sqlite:
      path: .knowl/knowl.sqlite
  server:
    listen_addr: 127.0.0.1:8080
  operator:
    token: ${KNOWL_OPERATOR_TOKEN}
```

Public scope names use lowercase letters, digits and hyphens, begin with a
letter or digit, and are at most 63 characters. `default_scope` must name an
enabled entry. `ref` is an optional trusted durable identity; omit it for a new
named scope. References must be unique and are never accepted from public
requests.

Do not combine the legacy top-level `knowl.sources` list with `knowl.scopes`.
Configuration is the administration boundary: the public HTTP, MCP and UI
surfaces do not create, rename, delete or enumerate scopes.

## Upgrade without moving the default scope

A legacy configuration without `scopes` continues to use its existing
workspace root and durable `knowl.scope` value. When explicit scopes are added
to an existing workspace:

- the existing default remains at the workspace root;
- named scopes use `scopes/<name>/`;
- accepted evidence, page history and operation identity are not rewritten.

A clean installation with explicit scopes uses `scopes/default/` and sibling
named directories. Knowl records the chosen layout under host control state so
restarts do not reinterpret it. If both the root and `scopes/default/` contain
possible default-scope canonical data, startup fails closed as ambiguous.

There is no required or automatic layout migration. No normalization command
is currently part of the multi-scope workflow. Supporting the legacy root is a
permanent resolver responsibility, not a temporary upgrade step.

## Select one scope

The global CLI flag applies to commands that read or mutate scope state:

```bash
knowl validate
knowl --scope infrastructure validate
knowl --scope infrastructure source sync --all
knowl --scope infrastructure run --source handbook
knowl --scope infrastructure hierarchy reconcile
knowl --scope infrastructure export okf --output ./infrastructure-okf
knowl --scope infrastructure export llms-txt --output ./infrastructure-llms.txt
```

Omitting `--scope` selects `default_scope`. Bootstrap creates a fresh host
workspace and therefore accepts only the configured default scope; configure a
named scope and its sources instead of bootstrapping into it.

HTTP uses a query selector for reads and a body selector for ingest:

```bash
curl -fsS --get \
  -H "Authorization: Bearer ${KNOWL_OPERATOR_TOKEN}" \
  --data-urlencode 'scope=infrastructure' \
  --data-urlencode 'query=database recovery' \
  http://127.0.0.1:8080/v1/retrieve

curl -fsS --get \
  -H "Authorization: Bearer ${KNOWL_OPERATOR_TOKEN}" \
  --data-urlencode 'scope=infrastructure' \
  http://127.0.0.1:8080/v1/pages/concepts%2Fdatabase-recovery

curl -fsS \
  -H "Authorization: Bearer ${KNOWL_OPERATOR_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"scope":"infrastructure","content":"Recovery uses WAL replay.","origin":"runbook","idempotency_key":"recovery-v1"}' \
  http://127.0.0.1:8080/v1/ingest

curl -fsS --get \
  -H "Authorization: Bearer ${KNOWL_OPERATOR_TOKEN}" \
  --data-urlencode 'scope=infrastructure' \
  http://127.0.0.1:8080/v1/operations/OPERATION_ID
```

The four MCP tools accept the same optional public `scope` field. Page and
operation status are always read through the selected scope, so an ID from
another scope is not disclosed. Supplying malformed, unknown, disabled or unauthorized scope
names returns the bounded `scope_forbidden` class before scoped data is read.

The Web UI keeps one scope in its URL and protected fragment requests. Open,
for example, `/ui/knowledge?scope=infrastructure`. The shell displays the
current public scope name, but it does not expose a scope directory. Direct URL
manipulation still passes through trusted resolution and authorization.

Authentication and scope authorization are separate boundaries. The initial
built-in policy authorizes the authenticated operator for configured scopes,
while every CLI, HTTP, MCP and UI operation still invokes the scope authorizer
with its read or write kind.

## Readiness and failures

`GET /readyz` reports the public default plus bounded per-scope state:

```json
{
  "service": "knowl",
  "status": "ready",
  "scope": "default",
  "scopes": [
    {"name": "default", "ready": true},
    {"name": "infrastructure", "ready": false, "reason": "unavailable"}
  ]
}
```

A canonical failure isolated to one scope leaves healthy scopes available and
marks only the failed scope unavailable. A shared database or untrustworthy
layout can make the whole host not ready. Readiness never returns durable scope
references, filesystem paths or page content.

Workers and the operational store are host-wide. Ready work is admitted fairly
across scopes, and `knowl.workers` is a total host limit rather than a limit per
scope. Publication remains ordered within one scope while disjoint scopes may
publish concurrently.

## Export, backup and downgrade

`knowl --scope NAME export okf` exports exactly one directly portable OKF v0.2
bundle. It contains the selected scope's `wiki/` content, without the public
scope name, adjacent scopes, raw evidence or host control state.

An export is not a complete operational backup. For a restorable host backup:

1. stop all writers and let active work drain;
2. copy the shared SQLite database, or take a consistent PostgreSQL backup;
3. copy the complete workspace, including the root default when present,
   `scopes/`, `.knowl/`, every scope's `schema.md`, `raw/` and `wiki/`;
4. preserve configuration and external provider credentials separately;
5. restore the database, workspace and configuration as one consistency set,
   then verify `/readyz` and `knowl --scope NAME validate` for every scope.

Do not run an older single-scope binary against a workspace after named scopes
have accepted data. Before a downgrade, stop writers and preserve every named
scope through a full backup or individual OKF exports, then follow a separately
defined downgrade procedure. Knowl does not collapse named scopes into the
default or delete them automatically.

## Two-scope source-to-wiki walkthrough

Build `knowl`, create two source trees and save the configuration above with
paths relative to a fresh directory. Set `OPENAI_API_KEY`, `OPENAI_MODEL` and
`KNOWL_OPERATOR_TOKEN`, then run:

```bash
knowl init
knowl --scope infrastructure init

knowl run --source handbook
knowl --scope infrastructure run --source handbook

knowl validate
knowl --scope infrastructure validate

knowl retrieve 'product handbook'
knowl --scope infrastructure retrieve 'infrastructure handbook'

knowl export okf --output ./default-okf
knowl --scope infrastructure export okf --output ./infrastructure-okf
```

Each run accepts and maintains only the selected scope's `handbook` source.
Inspect the two exported `index.md` files and their pages independently. A query
never fans out to the other scope when its selector is omitted.
