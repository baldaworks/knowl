# Service operations

This document is the operator-facing reference for running Knowl as a service.

If you only need the product overview, start with [README.md](../README.md).
If you need the baseline container path, see [sidecar deployment](sidecar.md).
For project-local Codex setup and MCP stdio, see the [local Codex guide](local-codex.md).

## Runtime model

Knowl is a standalone knowledge service with:

- a canonical workspace;
- one ingest pipeline;
- rebuildable operational state and projections;
- MCP and HTTP transports over the same application services.

Baseline deployment is service/sidecar mode. Fx embedding is the alternative
for Go applications that want the same runtime in-process.

## Maintenance workers

`knowl.workers` defaults to `1`; set it to `2` to allow a second maintenance
operation to infer while the first waits for its model. Only integer `1` and `2`
are supported. Omitted means one; explicit zero, strings, fractions and larger
values fail preflight. This setting applies to serving and `knowl run` alike.

```yaml
knowl:
  workers: 2
```

Each worker owns an independent maintainer runtime and session. Two workers can
double the configured provider tree's process count, memory and inference cost.
A provider tree can itself contain multiple processes. Concurrent operations
may finish in a different order; this setting makes no throughput guarantee.
Programmatic `Options.Maintainer` is a singleton and requires capacity one.
A supplied `RuntimeFactory` must build a fresh agent for each owner.

The asynchronous scheduler, synchronous Drain/RunOnce, explicit hierarchy and
optional model-lint calls share the same execution capacity. There is no live
resizing. Direct hierarchy and model-lint admission include waiting in a total
five-minute bound, shortened by the caller's deadline and existing read limits.
Concurrent Drain invocations wait cancelably; each reports only its own claims.
Wake hints are bounded and lossy; accepted operations remain in durable storage
and periodic scans recover missed hints. Work is claimed only after admission.

Inference runs outside the canonical write lock. A shared publication gate
orders commit, full-snapshot projection and durable outcome across source
maintenance, hierarchy and source synchronization. Catalog and log preconditions
can conflict even when operations edit different factual pages. A stale plan
fails permanently with class `canonical_conflict` and reason
`precondition_failed`, preserving the first commit. Knowl does not automatically
rebase, replan or spend an output-correction turn on this conflict. Review the
current workspace before an explicit retry.

Stop first closes admission and new claims, then allows active owners to drain
within the active shutdown caller's bound. When that bound expires, Stop cancels
active executions and returns the context error. A concurrent Stop waiting for
shutdown serialization honors its own deadline and returns its context error
without canceling the first caller's graceful drain. Stop retains resources still
in use; a subsequent Stop joins exited owners and retries failed cleanup before
closing the store.
Providers and custom maintainers must cooperate with context cancellation.
A provider or custom maintainer that ignores cancellation can delay final
cleanup; the host does not force-close its live resources. Startup recovery
precedes worker admission.
Stop all writers before standalone projection rebuilds, migrations or backups.

To roll back capacity, stop and drain the old host, set `workers: 1`, then
restart. Preserve the complete workspace, immutable raw revisions, operational
database and recovery journal. Interrupted operations use normal durable recovery.

## Configuration

The CLI loads `.config/knowl/config.yaml` by default. `--config-dir` selects an
additional config root and `--profile` selects a top-level profile.

The config has two sections:

- `runtime:` — shared provider registry in Balda-compatible typed shape
- `knowl:` — Knowl application settings

SQLite example:

```yaml
runtime:
  providers:
    opencode:
      type: opencode_acp
      opencode_acp:
        model: opencode/big-pickle

knowl:
  provider: opencode
  workers: 1
  output:
    max_corrections: 1
  workspace:
    path: .
  storage:
    type: sqlite
    sqlite:
      path: .knowl/knowl.sqlite
  scope: local
  server:
    listen_addr: 127.0.0.1:8080
  web:
    enabled: false
  operator:
    token: replace-with-a-local-secret
```

PostgreSQL example:

```yaml
knowl:
  storage:
    type: postgres
    postgres:
      dsn: ${KNOWL_POSTGRES_DSN}
```

Container baseline example:

```yaml
knowl:
  workspace:
    path: /var/lib/knowl/knowledge
  storage:
    type: sqlite
    sqlite:
      path: .knowl/knowl.sqlite
  server:
    listen_addr: 0.0.0.0:8080
```

Two-source example with optional automatic startup sync disabled:

```yaml
runtime:
  providers:
    opencode:
      type: opencode_acp
      opencode_acp:
        model: opencode/big-pickle

knowl:
  provider: opencode
  workspace:
    path: /var/lib/knowl/knowledge
  sources:
    - id: engineering
      type: filesystem
      filesystem:
        root: /sources/engineering
        include: ["**/*.md"]
        flavor: obsidian
      sync:
        on_start: false
        interval: 5m
        retry_initial: 1s
        retry_maximum: 1m
    - id: operations
      type: filesystem
      filesystem:
        root: /sources/operations
        include: ["**/*.md"]
        flavor: markdown
      sync:
        on_start: false
        interval: 5m
        retry_initial: 1s
        retry_maximum: 1m
    - id: catalog
      type: filesystem
      filesystem:
        root: /sources/catalog
        include: ["**/*"]
        flavor: okf
      sync:
        on_start: false
        interval: 5m
    - id: handbook
      type: git
      git:
        remote: https://github.com/example/handbook.git
        ref: refs/heads/main
        ref_kind: branch
        include: ["docs/**/*.md"]
        flavor: markdown
        uri_base: https://github.com/example/handbook/blob
        auth:
          secret_env: HANDBOOK_GIT_TOKEN
        max_transfer_bytes: 524288000
        max_cache_bytes: 536870912
      sync:
        on_start: false
        interval: 5m
```

Notes:

- `knowl.provider` selects one entry from `runtime.providers`. A runnable host
  fails before readiness when no configured or explicitly injected maintainer
  is available.
- `knowl.storage.type` selects one optional typed storage block.
- when storage is omitted, Knowl defaults to SQLite.
- default local listen address is `127.0.0.1:8080`; service/sidecar deployments
  may override it with `0.0.0.0:8080` or another literal IP bind.
- when `knowl.operator.token` is non-empty, `/v1/*` and `/mcp` require an
  `Authorization: Bearer <token>` header. Health and readiness probes remain
  unauthenticated. Keep tokenless deployments on a trusted, loopback-only
  network boundary.

Git sources use the same `source list`, `source sync`, `source status`,
scheduling, retry, and maintenance lifecycle as filesystem sources. They only
read HTTPS or SSH remotes and pin a complete scan to one immutable commit.
Credentials must be external: `auth.secret_env` supplies an HTTPS token or SSH
private key, while `auth.key_file` supplies an SSH key file. SSH configuration
must also provide host-bound public keys in `known_hosts`. Do not place secrets
in `remote`, `uri_base`, or `repository_id`.

The default policy rejects non-fast-forward branch changes and moved tags while
preserving the last successful checkpoint and active catalog. Set
`allow_rewrite: true` to explicitly permit rewritten branch history. Set
`rebind_ack: true` only for the synchronization that intentionally adopts a new
repository identity or moved tag, then remove the acknowledgement. Git data
under `<workspace>/.knowl/cache/git/<source-id>` is a scoped bare cache: Knowl
fetches only the configured tracked ref, without unrelated branches or tags,
while retaining the ref's complete history and objects. The `include` patterns
filter document paths after that transfer; they do not provide partial-clone or
path-level network filtering. These directories are rebuildable cache, not
canonical evidence, and can be removed while Knowl is stopped. A cache created
by an older Knowl version may retain previously mirrored objects until it is
removed, but subsequent refreshes use the scoped tracked-ref fetch.
The defaults cap one pack transfer at 500 MiB and one source cache at 512 MiB;
override `max_transfer_bytes` and `max_cache_bytes` within the documented 8 GiB
maximum when repository size requires it.

Common `KNOWL_*` overrides include:

- `KNOWL_PROVIDER`
- `KNOWL_WORKSPACE_PATH`
- `KNOWL_STORAGE_TYPE`
- `KNOWL_STORAGE_SQLITE_PATH`
- `KNOWL_STORAGE_POSTGRES_DSN`
- `KNOWL_SERVER_LISTEN_ADDR`
- `KNOWL_OPERATOR_TOKEN`

## Optional web UI and operator reads

The web UI and operator read API are disabled by default. Add this overlay to
an existing valid provider/workspace configuration to enable them:

```yaml
knowl:
  web:
    enabled: true
  server:
    listen_addr: 127.0.0.1:8080
  operator:
    token: ${KNOWL_OPERATOR_TOKEN}
```

Set `KNOWL_OPERATOR_TOKEN` to a local secret before starting the service. Keep
secret overrides in `.config/knowl/*.local.yaml` or the process environment;
do not commit them. Startup rejects enabled web access with an empty token.
For embedded Go applications the corresponding fields are `Config.Web.Enabled`
and `Config.OperatorToken`. MCP stdio disables web access.

Open `http://127.0.0.1:8080/ui/` and enter the token in **Connect**. The public
shell and bundled assets contain no workspace data; fragments under
`/ui/fragments/*` and JSON reads under `/operator/v1/*` require the bearer
header. Disabled web routes return 404. Health probes remain public.

The browser keeps the token in memory for the current document. Reload,
**Disconnect**, or an authorization failure clears the connection. It is not
saved in cookies, local/session storage, browser URLs, or exported search JSON.
Data responses use `Cache-Control: no-store`. Treat the operator token as a
write-capable credential: it also authorizes `POST /v1/ingest` and the existing
MCP tools. The read-only UI does not narrow the token's authority.

Keep local access on loopback. For remote access, terminate HTTPS at a trusted
reverse proxy and restrict direct access to the Go listener. Knowl serves HTTP
on this listener; it does not configure TLS certificates. Forward the bearer
header, and keep credentials and query strings out of proxy/access logs. Normal
browsing loads assets locally and does not automatically fetch upstream URLs
or remote images. A deliberate **Open original** click navigates to the allowed
HTTP/HTTPS URL without forwarding the operator token.

The [web UI guide](web-ui.md) covers all four screens and a source-to-wiki
walkthrough. The [operator OpenAPI contract](../api/openapi/operator.yaml)
defines seven read routes:

| Path (all `GET`) | Purpose |
| --- | --- |
| `/operator/v1/catalogs` | Read direct catalog children; optional `parent_id` |
| `/operator/v1/pages` | List current factual page summaries |
| `/operator/v1/page?page_id=...` | Read one current canonical page |
| `/operator/v1/source-revision?source_ref=...` | Read immutable accepted text |
| `/operator/v1/sources` | List configured sources and saved status |
| `/operator/v1/sources/{source_id}` | Read source status and document inventory |
| `/operator/v1/operations` | List stored operations; optional `status` and `source_id` |

Lists default to 50 items, accept `limit` from 1 through 100, and return an
optional `next_cursor`. The UI starts Operations at 10 rows. Continue with the
same route, filter, and limit; cursors are opaque, bounded to 8 KiB, and become
invalid after a host restart. Canonical catalog/page continuations also bind to
the current snapshot. Refresh from the first page if it changes. Requests
cannot select another scope or workspace. Unknown/duplicate parameters, GET
bodies, and total query strings over 16 KiB are rejected. Bounded workspace
reads use the existing read limits; an unavailable saved source is reported
without fetching an upstream substitute.

Source synchronization, accepted data, and completed knowledge processing are
separate facts. A successful sync may still have queued or failed maintenance;
a later failed scan preserves prior accepted revisions and the last successful
sync. Only a stored reconciled tombstone confirms deletion. Operations show
saved attempt reports where available; selected context is what the maintainer
read, not a changed-page list. Older operations may have no reports. Missing
reports mean unavailable, rather than failed retrieval or zero work.

## Optional embeddings

Embeddings are off by default. Disabled configuration creates no embedding
client, reads no embedding credential and makes no model/readiness request.
The maintainer selected by `knowl.provider` remains a separate requirement.

This opt-in fragment matches the checked-in CPU sidecar profile:

```yaml
knowl:
  embeddings:
    enabled: true
    endpoint: http://tei:80/v1/embeddings
    model: intfloat/multilingual-e5-base
    revision: d128750597153bb5987e10b1c3493a34e5a4502a
    dimensions: 768
    query_prefix: 'query: '
    passage_prefix: 'passage: '
    failure_policy: lexical
```

`endpoint` is the full URL for one OpenAI-compatible float embedding API. Use
`api_key_env: YOUR_EMBEDDING_KEY` to read an optional bearer credential from the
process environment. It names the variable, not its value; a missing configured
credential fails startup. Enabled configuration requires model/revision and
1–4,096 dimensions. Model/revision/prefix fields are bounded at 256 UTF-8 bytes;
the endpoint at 2,048. Empty prefixes are supported for models that require none.
Userinfo and fragments in endpoints are rejected. HTTPS uses normal TLS;
private HTTP is supported. Redirects and inference retries are disabled.

The operator must keep the declared immutable revision aligned with the served
weights. The response model name and vector dimension are checked; the generic
API cannot attest weight identity. Change the declared revision and rebuild
when weights change. The pinned reference is
[E5-base](https://huggingface.co/intfloat/multilingual-e5-base), served by the
[CPU TEI sidecar](sidecar.md#optional-cpu-embeddings) with mean float32 output,
required prefixes and server truncation disabled.

| Setting or fixed bound | Behavior |
| --- | --- |
| `failure_policy: lexical` | Classified embedding failure returns lexical evidence with `degraded` mode and a safe reason |
| `failure_policy: strict` | Missing/unavailable/incompatible dense retrieval returns a typed failure |
| Request | At most 16 inputs, 2,048 UTF-8 bytes each including prefix, 64 KiB serialized body |
| Response | At most 1 MiB; complete unique indices, exact model/dimensions, finite nonzero normalized vectors |
| Inference | 10-second request bound or earlier caller deadline; four concurrent client requests, no hidden retry |
| Semantic input | NFC/original case; 384 runes per chunk, 64 overlap; 16 chunks/page, four/query or source signals |
| Projection | At most 8,192 chunks and 64 MiB per scope; 15-minute rebuild or earlier caller deadline |

Rune limits are not tokenizer limits. The service must reject token overflow
rather than silently truncate; the pinned TEI reports `input_limit`. Chunk/rune
coverage omissions appear in the Go report. Lexical full-field indexing and
maintenance's complete-page/request limits still apply. Larger corpora require a
separately evaluated capacity design; these bounds are not throughput promises.

HTTP/MCP results optionally include, for example:

```json
{"retrieval":{"effective":"degraded","reason":"unavailable"}}
```

Effective modes are `lexical`, `hybrid`, `degraded`, and `failed`. Go
`QueryResult.Retrieval`, `IngestResult.Retrieval` and durable
`Operation.Retrieval` additionally carry requested mode, model-space prefix,
candidate/scanned-chunk counts and omitted coverage. Maintenance records its
selection report before maintainer inference. `Operation.RetrievalAttempt`
identifies its originating work attempt; terminal/legacy replay does not invent
or replace historical reports. Reports omit text, endpoint URLs, credentials and
upstream error bodies. Public transport exposes only effective mode and reason.

A rebuild replaces lexical state first, then generates vectors without holding
SQL locks. Complete dense publication checks the canonical snapshot again;
concurrent change discards the stale build. A classified failure saves degraded
state even under strict policy. With lexical fallback, startup may be ready
while semantic retrieval is degraded; `/readyz` alone is not a hybrid guarantee.
Read the retrieval status. Invalid input and caller cancellation never return
successful fallback.

Queries never trigger a rebuild or download a model. After repairing a degraded
service, restart Knowl to retry the projection once during startup. Embedded
applications can call `knowl.RebuildProjection(ctx, config, snapshot)` explicitly.
Changing model/revision/dimensions/prefixes requires a complete projection rebuild
and changes maintenance identity. A strict projection error can occur after a
canonical commit: repair the derived projection, preserving the committed
facts and raw evidence.

Migration 16 adds disposable vector state and bounded operation reports in both
stores. Down removes those additions, preserving canonical/raw content and
older durable state. Stop writers and pair the schema with a compatible binary
before rollback. Disabling embeddings retains the default lexical behavior and
ignores derived vectors. See [pending-operation recovery](#upgrading-pending-operations)
for generation changes.

## Supported operator workflow

Local workspace bootstrap:

```bash
go build -o knowl ./cmd/knowl
./knowl bootstrap wiki /path/to/wiki
# or: ./knowl bootstrap obsidian /path/to/vault
# or: ./knowl bootstrap okf /path/to/okf-bundle
```

Bootstrap retains its fresh-workspace guard but now performs exactly one shared
source sync using ID `bootstrap-wiki`, `bootstrap-obsidian`, or `bootstrap-okf`.
The `okf` flavor validates and preserves OKF v0.2 metadata, reserved controls,
Unicode paths, and standard concept links. A missing version is treated as v0.2;
another declared version is consumed best-effort and reported in the sync
result under `diagnostics`. A newly generated config includes a provider and
retains that source for later operation. If an operator-owned config already
exists, bootstrap does not rewrite it; add the source entry there before using
ongoing source commands. Ordinary source sync accepts existing workspaces,
stores source revisions only in `raw/`, and queues durable maintainer
operations. It never copies source content into `wiki/`. Bootstrap is optional;
an operator may initialize an empty workspace and sync configured sources later.

To convert a legacy canonical workspace, stop active writers, back it up, and
run `./knowl migrate okf-v0.2`. Migration is explicit and idempotent; startup,
`retrieve`, and `source status` never perform it. Validate and inspect retrieval
afterward before retiring a backup. See [workspace semantics](workspace.md) for
the recovery and archive contract.

The operational-store migration that adds generic hierarchy operations is
additive in both SQLite and PostgreSQL and leaves existing source operation IDs,
descriptors, leases, and statuses unchanged. Downgrading to an older Knowl
binary is safe only before any hierarchy operation row exists. After the first
reconcile, restore the pre-upgrade operational database for a binary rollback;
the canonical Markdown workspace remains portable and can rebuild a fresh
projection.

Empty workspace initialization:

```bash
./knowl init
./knowl validate
./knowl start
```

Sidecar baseline:

```bash
docker compose -f deploy/sidecar/compose.yaml up --build
```

The CLI commands `retrieve`, `ingest`, and `operation` are one-shot operator
wrappers over the same service semantics. Source controls run directly against
an in-process Host without starting HTTP or scheduled runners:

```bash
./knowl source list
./knowl source sync engineering
./knowl source sync --all
./knowl source status engineering
./knowl source retry engineering --failure-class provider --dry-run
```

To run a complete one-shot knowledge processing cycle (synchronize sources, drain
queued maintenance operations to completion, and optionally reconcile semantic OKF
hierarchy) without running a persistent daemon:

```bash
./knowl run
# or restrict to one source:
./knowl run --source engineering
# or skip optional phases:
./knowl run --no-sync
./knowl run --no-hierarchy
```


To explicitly replace a valid flat root with source-independent semantic
catalogs, stop other writers and run:

```bash
./knowl hierarchy reconcile
./knowl validate
```

This is the only hierarchy-specific mutation command. It constructs the normal
provider, workspace, and selected store in process, claims exactly its reserved
hierarchy operation, and does not start HTTP, the general operation scheduler,
source jobs, or configured `on_start` synchronization. Output is structured
JSON. A changed result includes its generation and affected catalog/log files;
a converged replay returns `"changed":false` and leaves the canonical digest
unchanged.

Planner identity includes `hierarchy-v3` and the effective output policy in the
durable operation identity. Planning uses deterministically ordered,
bounded page metadata, excerpts, current memberships, and the schema digest;
schema content, raw source bodies, provenance, and source-native paths are not
taxonomy input. The digest binds the operation to the current operator policy;
it does not cause hierarchy planning to interpret that policy. The maintainer
treats type and technology as supporting signals, recursively
decomposes broad heterogeneous subjects, permits sparse secondary membership for
cross-cutting pages, and tries to reuse suitable current semantic structure.
The same bounded output correction applies as for source planning.
Semantic quality remains provider-dependent. Only this explicit command can
apply the result; startup and source synchronization do not reconcile catalogs.

Generated hierarchy controls are restricted to `wiki/index.md` and
`wiki/catalogs/**/index.md`. Ordinary concepts and `raw/` evidence are preserved
byte-for-byte. Planning is all-or-nothing and bounded: 1,024 pages, 1,024
catalogs, 16,384 edges, depth 16, 4 MiB input, 4,096 excerpt characters per
page, 1 MiB plan output, 1,024 edits, 256 KiB per catalog, and a 1 MiB manifest.
The command fails closed on a stale snapshot, invalid/incomplete graph, unsafe
path, an empty generated non-root catalog, or exceeded value and returns the
wrapped cause. An empty root is valid only for an empty wiki. See
[workspace semantics](workspace.md#explicit-semantic-hierarchy-reconciliation)
for ownership and recovery details.

These commands are operator conveniences, not the primary agent integration
surface. Source management is not exposed as an MCP tool.

`on_start` attempts are asynchronous. Each enabled source has independent
interval and capped retry state; a source cannot overlap itself, while different
sources can progress independently. A failed source leaves readiness and last
successful retrieval snapshots available. Inspect `source status` for durable
last-attempt/last-success state. After restart, recovery converges staged source
work before readiness.

A successful sync reports raw acceptance and maintenance reservation, not LLM
completion. `source status` reports bounded maintenance counts and samples for
queued, retrying, replayed, committed, and failed operations. Each sample
correlates the source document/revision with its operation ID. It also reports
the bounded `generation_prefix`, `work_attempt`, `retry_attempt`,
`manual_retry_count`, and, when applicable, `failure_class`, a stable safe
`failure_reason`, and `next_retry_at`. Source
bodies, prompts, provider error text, credentials, and raw provider output are
never included.

The default per-document maintenance read ceiling is 262,144 characters, still
bounded by the 4 MiB per-document byte limit. Changing that ceiling or another
output-affecting maintenance rule changes the policy generation and makes an
unchanged document eligible once under the new generation.

### Source maintenance context and navigation

Source maintenance v6 separates complete catalog navigation from selected
factual pages. The factual read limit defaults to 20 pages; catalog count does
not consume that allowance. The model receives `catalogs` as compact
path/digest/title/children nodes and `catalog_limits` as effective bounds.
Catalog Markdown is retained by the application, which preserves it when
rendering additive navigation.

`pkg/knowl/types.CatalogLimits` has these finite local defaults:

| Go field / JSON field | Default | Measures |
| --- | --- | --- |
| `MaxCatalogs` / `max_catalogs` | 1,024 | All catalogs, including root |
| `MaxEdges` / `max_edges` | 16,384 | Unique internal child destinations per catalog, summed across catalogs |
| `MaxDepth` / `max_depth` | 16 | Longest catalog chain, including root |
| `MaxPathBytes` / `max_path_bytes` | 2,048 bytes | Canonical catalog and child paths |
| `MaxCatalogBytes` / `max_catalog_bytes` | 262,144 bytes | Each original or rendered catalog |
| `MaxSnapshotBytes` / `max_snapshot_bytes` | 4,194,304 bytes | All original or rendered catalog Markdown combined |
| `MaxInputBytes` / `max_input_bytes` | 4,194,304 bytes | Serialized JSON catalog graph |

These are ceilings, not measured production capacity. Catalog input is complete
or rejected before inference; it is never truncated to fit. Proposed additions
must satisfy final graph bounds. Generated catalog files and factual edits also
share the plan defaults of 32 files and 256 KiB per file. Filesystem validation
and preconditions still apply.

Embedded Go callers set `knowl.Config.IngestOptions.CatalogLimits` using
`pkg/knowl/types.CatalogLimits`. An entirely zero value selects all defaults.
For custom limits, supply all seven positive fields; partially populated values
are invalid, and `MaxPathBytes` cannot exceed 2,048. The CLI uses the defaults;
its YAML does not expose `knowl.ingest` or `knowl.maintenance` sections.

Update custom maintainers and supplied-plan callers for `source-maintenance-v6`.
Ordinary page edits still carry schema/source provenance and existing digests.
Use `catalog_additions` for navigation. For example, this fragment adds a new
subject catalog and links an ordinary page created in the same plan:

```json
{
  "catalog_additions": [
    {
      "path": "wiki/index.md",
      "expected_digest": "<copy the root digest from input.catalogs>",
      "children": ["wiki/catalogs/storage/index.md"]
    },
    {
      "path": "wiki/catalogs/storage/index.md",
      "title": "Storage",
      "children": ["wiki/entities/storage.md"]
    }
  ]
}
```

A complete source response also includes `schema_digest`, `source_refs` and
`edits`. Existing catalogs require the exact original digest and omit `title`;
new catalogs omit the digest and require a nonempty single-line title and
children. Membership already present produces no catalog mutation. Ingest
supports additions; use explicit hierarchy reconciliation to restructure
navigation. A raw catalog FileEdit is rejected rather than converted.

#### Complete source request budget

The default and supported local maximum is **4,194,304 UTF-8 bytes (4 MiB)** for
the complete current source-maintenance user prompt. With the built-in runtime,
this includes the JSON envelope, full source, schema (including base64 for its
byte content), catalog graph, selected page metadata/provenance, instructions,
input/output schemas and structured-wrapper framing. JSON escaping counts.
This is not a token limit, backend HTTP payload measurement or session-history
capacity guarantee. Hierarchy has separate graph bounds and checks its complete
provider request, including correction feedback, against the runtime's input cap.

Embedded Go callers may set `knowl.Config.IngestOptions.InputLimits`, using
`pkg/knowl/types.MaintenanceInputLimits{MaxRequestBytes: ...}`. Zero selects the
default; custom values must be positive and at most 4,194,304. The effective cap
is the minimum of this value and the maintainer's declared capacity. The CLI
uses defaults and exposes no input-budget YAML option.

The built-in runtime reserves **128 bytes** within this cap for correction
feedback, including when extra turns are disabled. Initial request bytes plus
the reserve must fit. Diagnostics report the actual initial `used_bytes` and
the original full `max_bytes`; the reserve is not counted as bytes sent.
The application measures complete indispensable source/schema/catalog input
first. If it cannot fit with the declared reserve, the operation fails permanently
with class
`input_budget` and reason `required_input_limit` before factual reads, inference
or staging. Immutable raw is retained; canonical files are unchanged. Increase
a smaller embedded cap within the supported ceiling or reduce the indispensable
input before explicitly reserving work under the new policy. Nothing is
silently truncated.

Otherwise, first-seen ordinary candidates are read in existing context priority
order under the normal per-page/count/deadline limits. Controls and duplicates
do not consume the factual page allowance. Up to that many unique candidates
are considered, including omissions. Only complete fitting snapshots are kept;
a large omitted page does not prevent a later smaller page from fitting.
Read/parse failures remain errors. Serialized factual `Content` appears once;
`Body` remains in application snapshots and is omitted from the source wire.

Existing factual replacements require a complete included snapshot and its exact
`expected_digest`, including supplied `FilePlan` calls. An omitted existing page
cannot be edited even if its current digest is known. New pages retain existing
provenance, reachability and create preconditions; commit still rejects a later
human edit. Omission can reduce recall and does not guarantee semantic duplicate
or contradiction detection.

The built-in runtime implements the optional `app.MaintenanceRequestSizer` port.
`RequestBudget()` declares positive `MaxBytes`, bounded `ReservedBytes` and a
non-secret, nonempty printable
`FormatVersion` of at most 256 UTF-8 bytes, captured once at service construction.
`RequestBytes(ctx, input)` must measure the complete current request without
building a runtime, downloading or inferring. Invalid declarations fail before
source acceptance/reservation. A plain custom maintainer without this port
assumes `app.EncodeSourceMaintenanceRequest`'s shared JSON envelope only. Custom
maintainers that add a wrapper must implement the port to cover its full size.
The built-in runtime compares the actual wrapped prompt with the effective cap
before invoking its inner agent; unexpected wrapper overflow remains a safe
`provider_input_limit` failure.

`IngestResult.Budget` carries transient typed `MaxBytes`, `UsedBytes`,
`IncludedCount` and `OmittedCount` when exact accepted-request or partial-prefix
usage was measured, including failed preparation or provider calls. It contains
no raw text or prompt. Saved-stage/terminal replay need not reconstruct this
compatibility field. The stored `details.context.budget` exposes measured fitting
facts through HTTP/MCP and can represent an unknown exact size; see
[operation details](#operation-details). The budget bounds the current request,
not total process memory: workspace inspection still snapshots canonical content
and encoding uses temporary allocations.

#### Source signals

Context selection uses parsed title, tags, headings and the bounded beginning
of eligible prose. A source may start with exact `---` delimiter lines around
YAML frontmatter (LF or CRLF). `title` must be a string and `tags` a sequence of
strings; other keys are ignored after structural validation. It need not be a
complete OKF document. For example:

```markdown
---
title: Session retention
tags: [storage, durability]
---
# Operational decision

Keep session records durable across restarts.
```

A usable metadata title wins; otherwise extraction uses the first eligible
ATX heading (`#` through `######` followed by an ASCII space/tab), Setext heading
(a prose line followed by an `=` or `-` underline), or first eligible nonempty
prose line. Backtick/tilde fences of at least three markers at up to three
ASCII spaces of indentation exclude code until a matching closer of at least
the opening length. Indented code (four columns, with four-column tab stops)
is also excluded. Markdown structure uses ASCII spaces/tabs; the scanner does
not implement full CommonMark, HTML interpretation or inline rendering.

Malformed YAML, duplicate/non-string keys, aliases, merge keys, invalid
`title`/`tags` types, extra YAML documents or exceeded metadata bounds discard
all metadata signals and fall back to Markdown. A closed metadata block is
excluded from prose even when invalid. Unterminated frontmatter is treated as
ordinary Markdown; delimiter and horizontal-rule lines are excluded from
signals. Invalid UTF-8 fails before inference with class `source_signals`,
preserving the accepted immutable raw revision.

| Signal / parser limit | Fixed ceiling |
| --- | --- |
| Source read (defaults) | 4 MiB and 262,144 characters per document, under the configured read deadline |
| YAML metadata | 262,144 bytes, 16,384 nodes, nesting depth 64 |
| Title / each tag / each heading | 256 Unicode runes |
| Tags / headings examined | First 32 entries in each list |
| All semantic signals combined | 4,096 runes and 16,384 UTF-8 bytes |
| Lexical query | 32 distinct terms and 256 total term runes |

Signals retain first-seen spelling/order; tags and headings are deduplicated.
The combined budget is consumed in title, tags, headings, body order, and cuts
preserve UTF-8 boundaries. Useful evidence late in a source may be omitted.
These are bounded local defaults, not a semantic-recall guarantee. Fixed
signal/parser/query limits have no YAML settings. `SourceText` sent to the
provider is the complete accepted text within its separate read limits.

SQLite and PostgreSQL share this query policy. Source ID/adapter are used only
when semantic fields produce no usable lexical terms, not when a nonempty
query has no matches. Retrieval still uses the existing neighbor/root/recent
page budget and one generic multilingual path.

Embedded indexes receive the optional `SourceSummary.Tags`, `Headings` and
`Body` fields. Custom indexes must consume them to reproduce this selection
behavior. The slice fields make `SourceSummary` non-comparable in Go; callers
that previously used `==` must compare its fields explicitly.

#### Generic literal retrieval

SQLite and PostgreSQL use one fixed word policy: Unicode letters/numbers with
attached combining marks, normalized with NFC, generic lowercase, then NFC.
English/Russian case and canonical accents match; accents remain distinct.
`what` and `why` are ordinary searchable words. Full case folding, stemming,
transliteration and language-specific paths are outside this literal policy.
Inflection, plurals and paraphrases are measured limitations of lexical-only
retrieval. Normalization does not provide semantic recall; optional embeddings
add a semantic candidate channel independently.

Native indexes receive private reversible tokens for the normalized complete
words. Original title, tags, description, body and provenance stay intact;
retrieval excerpts use original character spans rather than native snippets.
Field priority remains title, tags, description, body. AND matches precede OR
fillers, duplicate pages are removed, and score ties use paths. Native scores,
rankings and PostgreSQL position limits can differ between backends.

Fixed direct-index bounds have no YAML settings:

| Input / result | Supported bound |
| --- | --- |
| Query passed to the index | Valid UTF-8, at most 65,536 raw bytes |
| Query terms | 32 distinct terms, 256 normalized runes combined |
| Examined raw source signals | Valid UTF-8, 65,536 bytes combined before clipping; first 32 tags and headings |
| Original semantic field | Valid UTF-8, 4,194,304 bytes per title/tags/description/body field |
| Derived page token streams | 524,288 bytes across all four fields; 8,192 distinct supported words |
| Queryable word | At most 256 normalized runes; longer words are excluded whole from the index |
| Source filter | At most 256 raw entries and 16 distinct validated source IDs, sorted/deduplicated |
| Direct-index snippet | Nonpositive character limit defaults to 4,096 runes; positive limits clamp at 262,144 |

`QueryService` retains its existing positive default read limits. The direct
snippet default bounds embedded calls that previously returned an entire page.
Invalid queries/source summaries and invalid projections return stable typed
errors; adapter `ErrInvalidProjection` aliases the shared projection sentinel.
A dense page can fit a file read yet exceed the derived limit. Rebuild then
fails atomically, preserving the previous scoped index/readiness and canonical
content. Supported words are never silently truncated to publish readiness.
These bounds are finite local ceilings, not production capacity measurements.

Migration `00015_generic_lexical` clears old FTS tokens / PostgreSQL vectors and
projection readiness, preserving original pages, links, provenance, operations,
source state and raw/canonical files. Startup rebuilds the derived projection
before serving readiness. PostgreSQL reuses its existing GIN index and stores
the vector explicitly with original values; it adds no encoded text columns.
On failure, correct the offending canonical page or configuration and retry the
existing rebuild/startup path. Down invalidates readiness; PostgreSQL restores
the earlier generated A/B/C/D vector, while SQLite recreates empty FTS. Stop
writers, pair the earlier binary with its schema/policy and rebuild before
resuming. Preserve raw and operation history.

#### Upgrading pending operations

Source-maintenance-v6 includes output-affecting retrieval policy in the maintenance
generation. Bounded correction additionally fingerprints effective support,
allowance, output/deadline limits and request reservation. The complete source
wire, request sizing and whole-page visibility
guards remain in force. Contract, schema, effective request cap/format identity,
read/plan/catalog limits, and embedding space/chunk/fusion/candidate/capacity/
failure policy identify the generation. Endpoints, credentials, CPU/runtime
settings and per-source measured usage are excluded.

Queued work that still needs a plan, including genuine v1-v5 and legacy
empty-generation work, fails before embedding or maintainer inference with class
`maintenance_policy` and reason `maintenance_policy_mismatch`. Automatic retry
and restart do not reinterpret its stored execution metadata.

Already validated concrete stages resume without new inference, including v1
catalog FileEdits and older policy stages. This also applies to authenticated
hierarchy stages after a planner/output-policy change; old hierarchy work without
a stage fails the descriptor guard before inference. Schema, provenance, original
digests and
atomic commit remain enforced. Their projection recovery publishes lexical
state without calling embeddings; dense repair is separate. Terminal replay
returns persisted outcomes and historical reports when present. Stale or corrupt
stages still fail under existing recovery rules.

For configured sources, inspect the mismatch and explicitly reserve work under
the current policy through the existing retry command:

```bash
./knowl source status engineering
./knowl source retry engineering --failure-class maintenance_policy --dry-run
./knowl source retry engineering --failure-class maintenance_policy
```

For a public text/URI ingestion, resubmit the same immutable input with the same
origin and idempotency key after updating the provider. Current generation
participates in reservation identity, so this creates or replays current-policy
work while preserving the historical operation and raw bytes. Embedded callers
can use the existing accepted-source reservation seam. No descriptor rewriting
or raw deletion is required. Stop writers before rolling back. Pre-v2 binaries
lack this mismatch guard and must not process newer queued work. A rollback
build that resumes operations must retain the compatibility checks; concrete
stages still require canonical preconditions. Preserve stored descriptors and
raw history.

Committed operations may include bounded `diagnostics` with only a stable
`code`, canonical `path`, and optional normalized `target`. An unresolved wiki
link is preserved only when the same normalized target exists in the accepted
immutable source; it is reported as `link.original_unresolved`. A generated
link without that source evidence remains invalid. If one generated document
has invalid provenance, Knowl withholds that document and any candidate catalog
closure that depends on it, records document-specific diagnostics, and may
commit and index the remaining safe subset. If no safe edit remains, or a
plan-wide/schema/path/bound/graph invariant fails, the operation fails without
publishing canonical content.

#### Bounded output correction

`knowl.output.max_corrections` accepts integer **0** or **1**. Omission defaults
to **1**; **0** permits only the initial generation. The same setting governs
source maintenance and explicit hierarchy planning. For example:

```yaml
knowl:
  output:
    max_corrections: 0
```

The built-in runtime can request one fresh complete replacement after malformed
JSON, schema/operation-branch rejection or full application-plan rejection.
These causes share one allowance: a valid first response uses one turn; a
replacement can make the total two. Every candidate passes the complete
application validator before staging. Correction uses the original captured
input and only one safe feedback code: `structured_output_invalid`,
`source_plan_invalid` or `hierarchy_plan_invalid`. Candidate text and validator
error messages are not copied into feedback. Existing validated safe-subset
behavior for document-specific warnings remains in force.

All turns share **1,048,576 output bytes (1 MiB)** and **five minutes** of total
planning time, including waiting for the runtime, lazy setup and inference.
These limits do not reset for a replacement. Earlier caller deadlines and
cancellation take precedence. Output accounting charges collector text across
turns, including streamed partial and final text; thought text is excluded.
Overflow reports only the accepted prefix. Input/transport/setup failures,
output overflow, timeout and cancellation do not trigger correction. Canonical
conflicts require existing explicit recovery; no automatic replan occurs.

Exhaustion, aggregate output overflow and the internal planning deadline are
permanent failures with reasons `provider_output_exhausted`,
`provider_output_limit` and `provider_output_deadline`. Earlier caller stops
retain their existing recoverability. Setting zero changes new-planning policy
identity; already authenticated stages still recover without inference.

Embedded callers use `Config.Output` or direct service options with
`types.OutputSettings`. Custom maintainers retain a single base call and report
`unavailable` physical counters unless they implement `app.ValidatingMaintainer`
or `app.ValidatingHierarchyMaintainer`. Supplied `FilePlan` is validated directly
and never invokes correction. See [operation details](#operation-details) for
durable evidence.

Transient provider build, transport, and execution failures are retried by the
operation scheduler. Each automatic retry cycle is limited to three total work
attempts. The first retry waits at least 30 seconds; later delays grow
exponentially with deterministic bounded positive jitter and never exceed five
minutes. The deadline and attempt counters are durable, so restarting Knowl does
not make work eligible early or reset its budget. Output correction happens
within one work attempt and does not increment scheduler retry counters.
Rejected output after the correction allowance is exhausted remains terminal;
the scheduler does not restart the correction sequence automatically.

`maintenance.counts.queued` covers non-terminal work that is ready now or is
currently claimed. `maintenance.counts.retrying` covers unleased non-terminal
work whose `next_retry_at` is in the future. `replayed` counts operations with
more than one durable work attempt and can overlap another outcome. `failed`
remains terminal until an operator explicitly recovers the operation.

### Recovering failed source maintenance

Deploy the corrected binary and let its additive store migration complete
before recovering historical failures. On each complete synchronization, Knowl
compares the current bounded maintenance-policy generation with the generation
stored for every unchanged document. An older committed operation or an older
terminal `source` failure receives one new current-generation operation.
`staging`, `provider`, unknown, and unrecognized failures remain unchanged until
an operator explicitly retries their class. Repeating a sync under the same
generation converges without creating work.

Reconciliation logs use the bounded fields `maintenance_trigger`
(`revision`, `policy`, or `unchanged`), `maintenance_outcome` (`queued`,
`replayed`, `converged`, or `manual_gate`), and a 16-character
`maintenance_generation`. They never include the policy payload, source body,
prompt, provider output, or credentials.

Start manual recovery with a class-filtered preview:

```bash
./knowl source status engineering
./knowl source retry engineering --failure-class provider --dry-run
```

The retry result is structured JSON with `source_id`, `dry_run`, `matched`,
`requeued`, `rejected`, `operation_ids`, and `truncated`. A preview returns the
same bounded eligible operation set without changing state, so `requeued` is
zero. Counts cover the complete match; `operation_ids` is limited to 100 entries
and `truncated` says whether more matched.

If the preview is expected, requeue exactly that failure class and observe it:

```bash
./knowl source retry engineering --failure-class provider
./knowl source status engineering
```

Repeat `--failure-class` to select more than one class only after each class's
root cause is fixed. A current-generation failure retries in place and preserves
its total work attempts. An older-generation failure creates one distinct
current-generation operation and leaves the historical failure unchanged. Both
paths start a fresh bounded retry cycle, increment `manual_retry_count`, and
wake the existing durable scheduler without starting source synchronization.
The request fails atomically, with a non-zero exit, if any selected candidate is
committed, stale, cross-scope, not source maintenance, or actively leased; no
candidate is requeued in that case. The JSON result still reports the bounded
candidate IDs and `rejected` count for safe diagnosis. Running the same command
again after a successful requeue matches no terminal operations and makes no
additional change.

Recover `provider` failures first. Re-run a preview and inspect status before
separately deciding whether corrected `source` or `staging` failures should be
requeued. Never use a broad multi-class retry merely to clear a red status.

For rollback, stop writers before reverting application binaries. The additive
generation columns and historical operations are safe to retain; do not remove
them while a newer writer may still create generation-bearing operations.

The maintainer builds one root-reachable semantic OKF wiki. Related evidence
from different sources may support the same page; retrieve returns all resolved
`source_documents`, and a source filter matches when any supporting document
belongs to that configured source. Legacy derived
`wiki/sources/<source_id>/**` trees are removed on that source's next successful
reconciliation without deleting raw history or curated pages.

## HTTP contract

Authoritative contract: [api/openapi/knowl.yaml](../api/openapi/knowl.yaml)

Business endpoints:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/v1/retrieve?query=...` | Retrieve bounded evidence with provenance |
| `POST` | `/v1/ingest` | Submit text or store a URI reference |
| `GET` | `/v1/operations/{operation_id}` | Read one durable public operation status |

Operational endpoints:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/healthz` | Process liveness |
| `GET` | `/readyz` | Workspace/store/projection readiness |

The public state model is:

```text
queued -> running -> completed | failed
```

The trusted scope is owned by the host or service configuration. Callers must
not supply a different scope through public request arguments.

## HTTP examples

Readiness:

```bash
curl -sS http://127.0.0.1:8080/readyz
```

Retrieve:

```bash
curl -sS \
  -H "Authorization: Bearer $KNOWL_OPERATOR_TOKEN" \
  "http://127.0.0.1:8080/v1/retrieve?query=Why%20was%20Badger%20chosen%3F"
```

Ingest accepts exactly one nonempty `content` or `uri`. Both HTTP and MCP trim
surrounding whitespace. Supplying both, or neither, is invalid.

Ingest text (default media type `text/plain`):

```bash
curl -sS \
  -H "Authorization: Bearer $KNOWL_OPERATOR_TOKEN" \
  -H "Content-Type: application/json" \
  http://127.0.0.1:8080/v1/ingest \
  -d '{
    "content": "Badger was chosen because ...",
    "origin": "ticket-1234",
    "idempotency_key": "ticket-1234"
  }'
```

Store a URI reference:

Knowl stores the URI string as the source body with default media type
`text/uri-list`. It does not download the page or follow redirects.

```bash
curl -sS \
  -H "Authorization: Bearer $KNOWL_OPERATOR_TOKEN" \
  -H "Content-Type: application/json" \
  http://127.0.0.1:8080/v1/ingest \
  -d '{
    "uri": "https://example.com/adr/session-memory-store"
  }'
```

Ingest page text that the caller has already obtained:

```bash
curl -sS \
  -H "Authorization: Bearer $KNOWL_OPERATOR_TOKEN" \
  -H "Content-Type: application/json" \
  http://127.0.0.1:8080/v1/ingest \
  -d '{
    "content": "# Session storage\n\nSessions use Badger for local persistence.",
    "media_type": "text/markdown",
    "origin": "https://example.com/adr/session-memory-store",
    "idempotency_key": "adr-session-store-v1"
  }'
```

`origin` is a source identity hint, not a fetch instruction or automatically
populated structured citation URI. Use an idempotency key for the specific
source revision; changed content needs a different key.

Ingest returns a durable `operation_id` and its current status. Maintenance
runs in the background. Poll the returned ID until `completed` or `failed`:

```bash
curl -sS \
  -H "Authorization: Bearer $KNOWL_OPERATOR_TOKEN" \
  http://127.0.0.1:8080/v1/operations/op_01K...
```

### Operation details

`GET /v1/operations/{operation_id}` and MCP `knowl_operation` return matching
optional `details`. Existing `id`, `status`, `updated_at`, `failure` and top-level
`retrieval` fields remain available; statuses are still `queued`, `running`,
`completed` and `failed`. A classified failure can additionally include a stable
`failure.reason`, such as `required_input_limit`, without provider error text.

| Field | Meaning |
| --- | --- |
| `context.work_attempt`, `outcome` | Producing work attempt and measured result: `assembled`, `selection_failed` or `assembly_failed`. |
| `context.candidate_count` | Unique ordinary candidates eligible within the factual page allowance. Excludes controls and duplicates; does not count every unselected wiki page. |
| `context.catalog_count` | Catalogs observed after successful workspace inspection. |
| `context.pages` | Canonical page IDs, first actual selection reason (`lexical`, `vector`, `hybrid`, `neighbor`, `recent`, `unknown`) and disposition (`included`, `budget_omitted`, `pending`). |
| `context.entries_omitted` | Entries left out of the diagnostic list because of reporting bounds. This does not remove pages from the model input. |
| `context.budget` | Serialized request `max_bytes`, measured `used_bytes` when known, and actual `included_count`/`omitted_count`. Pending candidates are not budget omissions. |
| `context.vector_projection` | `not_checked`, `ready` or `invalid` as observed during that attempt, with a safe reason when applicable. It is not current projection readiness. |
| `retrieval`, `retrieval_attempt` | Actual requested/effective modes, safe reason, candidate/scan/omission counts, optional model-space fingerprint and producing attempt. |
| `correction` | Producing `work_attempt`, `max_corrections`, `max_output_bytes`, `deadline_nanos`, outcome and optional measured `turns`, `corrections`, `output_bytes`, last `validation_code`. |
| `plan` | Stored validated/staged SHA-256 digest and `file_count` when known; no edits or rationale. |
| `warnings`, `warnings_omitted` | Bounded application warning codes and safe relative identifiers; omitted warnings are counted separately. |
| `execution` | Stored `work_attempt`, `retry_attempt`, `manual_retry_count`, `apply_attempt` and optional scheduling `ready_at`. Apply attempts are not inference-call counts. |

The context snapshot is finalized once per work attempt, after assembly and
before inference, or before returning a measured selection/assembly failure.
`assembled` means the request was assembled; inference, validation or commit may
still fail afterward. A read failure midway through fitting retains the last
successfully measured accepted prefix and leaves unprocessed entries `pending`.
Measured indispensable-input overflow may show `used_bytes` above the cap. If
serializer preflight stopped exact measurement, `used_bytes` is absent.

Missing values mean unavailable, including old rows, unsupported custom adapters,
failures before selection and crashes before snapshot finalization. A queued
operation can have zero execution counters without context or a plan. Retries can
retain a snapshot from an older attempt; compare its `work_attempt` and
`retrieval_attempt` with `execution.work_attempt`. Polling and terminal replay read
stored facts without selection, embeddings or inference.

The correction report is finalized once before an accepted plan is staged, or
before returning a planning failure. Outcomes are `accepted`, `exhausted`,
`output_limit`, `deadline`, `canceled`, `provider_failed` and `unavailable`.
`turns` counts actual inner generation calls, `corrections` counts started calls
after the first, and `output_bytes` counts accepted-prefix text across those
calls. A measured setup failure can have zero turns; a custom adapter with no
measurements omits all three counters. Compare its producing `work_attempt`
with `execution.work_attempt` when a historical report survives recovery.
A crash before finalization can leave correction absent. The built-in store
write has a five-second durable timeout; an accepted-report write failure stops
staging, and a rejected-report write failure preserves the original planning error.

Context reports are limited to 32 KiB and 100 listed candidates. Identifiers are
limited to 2,048 UTF-8 bytes. Retrieval retains its 2 KiB bound; warnings retain
their 64-entry/32 KiB bound. Complete serialized details stay below 96 KiB.
Correction adds at most 1 KiB of content-free facts within that combined bound.
Canonical page names are visible to callers already authorized for the trusted
scope. Source bodies, queries, prompts, edits, rationale, provider messages,
credentials and endpoints are excluded.

SQLite and PostgreSQL retain these facts across restart. Preserve the operational
database for historical diagnostics. Migration 17 adds nullable context and file
count columns; its downgrade drops those new facts while retaining plan digests,
existing reports/counters and canonical content. Old digest-only rows have an
unknown file count. Opaque legacy digests remain readable but have no public plan
summary. A failed context-report write stops new inference; if assembly also
failed, its original classified failure reason is preserved.

Migration 18 adds nullable `correction_report` with no historical backfill.
Its downgrade drops only these correction facts. Retain the database and use
the [pending-operation policy guards](#upgrading-pending-operations) when
rolling back; raw, canonical Markdown and authenticated stages are preserved.

## MCP contract

MCP is the primary agent-facing interface.

The running service exposes MCP Streamable HTTP on its existing listener at
`http://127.0.0.1:8080/mcp`.

When an operator token is configured, MCP clients must send the same bearer
token in the HTTP `Authorization` header.

The baseline server exposes exactly:

- `knowl_retrieve`
- `knowl_ingest`
- `knowl_operation`

MCP and HTTP call the same underlying application services.

For `knowl_ingest`, pass a URI reference as tool arguments:

```json
{
  "uri": "https://example.com/adr/session-memory-store"
}
```

To ingest the document's contents, pass its already obtained text instead:

```json
{
  "content": "# Session storage\n\nSessions use Badger for local persistence.",
  "media_type": "text/markdown",
  "origin": "https://example.com/adr/session-memory-store",
  "idempotency_key": "adr-session-store-v1"
}
```

The same exactly-one-payload and no-download rules apply. Poll
`knowl_operation` with the returned `operation_id`:

```json
{
  "id": "op_01K..."
}
```

## Lifecycle and readiness

Host construction performs:

1. workspace validation;
2. selected-store setup/migration;
3. recovery;
4. projection preparation;
5. listener startup.

`/healthz` only means the process is serving HTTP.

`/readyz` means:

- workspace is usable;
- store is open;
- recovery completed;
- projections are ready for retrieve/operation reads.

## Sidecar notes

The checked-in sidecar assets assume:

- Knowl owns `/var/lib/knowl`;
- the canonical workspace is `/var/lib/knowl/knowledge`;
- the agent talks to Knowl over MCP or the same KISS HTTP contract;
- the agent does not mutate `raw/`, `wiki/`, or `.knowl/` directly.

## Fx embedding

For Go applications:

- root `pkg/knowl` is the non-Fx runtime entrypoint;
- `pkg/knowlfx.NewApp` wraps the same runtime with Fx lifecycle management.

This is an alternative deployment/composition mode, not a second product API.
