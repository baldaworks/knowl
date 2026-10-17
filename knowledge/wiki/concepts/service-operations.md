---
type: topic
title: Service Operations
knowl:
  id: concepts/service-operations
  source_refs:
    - wiki-filesystem:knowl-docs/agents.md@d87e9854e3ae341cefe5b533604ec9d35aac50c8d89f91598cad158e58f09bb9
    - wiki-filesystem:knowl-docs/local-codex.md@fd3890adabd370f2b1d99a280bc9fd95564337ed871fe5be66c7b1f4ef085de6
    - wiki-filesystem:knowl-docs/local.md@b72ed8a3f8d255df1843e32445e50fa7be99e752b764711b7494cd76f325c5e0
    - wiki-filesystem:knowl-docs/operations.md@ae3c7b6ee1583fd88681e5a3ee4bb1cdf2a9b007235663169cf7e59a41b60baa
    - wiki-filesystem:knowl-docs/search.md@0c24dcc0dd1409c464a9bb667634d9b439b3bcbf2a6e29537981b0c753b00d74
    - wiki-filesystem:knowl-docs/service.md@646948819ed449ec0af05909f13981d098c69e75cd4e7c9b09749f4c76799754
    - wiki-filesystem:knowl-docs/sidecar.md@a49cb8614961df7fbb475965018e0531ca85dca60964863716d383e52b526f78
    - wiki-filesystem:knowl-docs/testing.md@9b27371ec8377d553b279f4be2f1304defc9e8b316ad2186379d24311dd85724
    - wiki-filesystem:knowl-docs/web-ui.md@87191e228d60139852c21653b38d3d02d1659893279ab85a9e9b0b51fef6aa85
---
# Service Operations

Knowl provides canonical workspace management, one ingest pipeline, rebuildable operational projections, and MCP/HTTP interfaces over the same application services. The CLI supports project-local execution; persistent deployments serve MCP/HTTP. Go applications compose the same runtime through `pkg/knowl` or Fx lifecycle integration through `pkg/knowlfx.NewApp`.

## Runtime and Deployment

- **Service storage**: Persist `/var/lib/knowl`, containing canonical workspace `/var/lib/knowl/knowledge` and SQLite state `/var/lib/knowl/knowledge/.knowl/knowl.sqlite`. Deployment assets remain under `deploy/sidecar/` for compatibility; this describes an arrangement rather than a separate mode.
- **Local Codex client**: Codex owns `npx --yes @baldaworks/knowl@0.7.1 mcp stdio` with the active project as working directory. This recipe requires the npm release to be published and needs no separate daemon.
- **Service clients**: HTTP uses `/v1/*`; MCP Streamable HTTP uses `http://127.0.0.1:8080/mcp`, `transport: "streamable_http"`, and `headers: {"Authorization": "Bearer <operator-token>"}`. Tools are `knowl_retrieve`, `knowl_ingest`, and `knowl_operation`.
- **Startup order**: Workspace validation, store setup/migration, recovery, projection preparation, then listener startup. Recovery precedes worker admission.
- **Probes**: `GET /healthz` means HTTP is serving. `GET /readyz` means the workspace is usable, store open, recovery complete, and projections ready for retrieve/operation reads. Probes remain unauthenticated.

## Project-Local Maintenance

The Codex maintainer quickstart requires Git, the Go version in `go.mod`, Node.js/npm, and an authenticated Codex CLI. Its local ACP bridge may download a pinned npm package on first startup. Consumer plugin installation is separate; its default configuration expects an installed and authenticated `opencode acp` maintainer.

The quickstart uses `.config/knowl/config.yaml`, `runtime.providers.codex.type: codex_acp`, `codex_acp.bridge_version: "1.7.3"`, `knowl.provider: codex`, `workspace.path: knowledge`, SQLite at `.knowl/knowl.sqlite`, `scope: local`, and `server.listen_addr: 127.0.0.1:0`. Optional `model` belongs under `codex_acp`.

One-shot commands need no persistent HTTP/MCP server or operator token:

- `knowl init` prepares workspace structure.
- `knowl run --source <id>` synchronizes evidence and executes maintenance.
- `knowl validate` checks workspace integrity and configuration.
- `knowl source status <id>` distinguishes completed maintenance from raw scan success.
- `knowl retrieve '<query>'` returns evidence with page identities, excerpts, and source refs through configured retrieval.

Accepted revisions live in `knowledge/raw/`; generated agent-selected titles and paths live in `knowledge/wiki/`.

## Maintenance Workers

`knowl.workers` defaults to integer `1` and accepts only `1` or `2`; explicit zero, strings, fractions, and larger values fail preflight. It governs both serving and `knowl run`, with no live resizing. Two owners allow concurrent inference and can double provider-tree processes, memory, and inference cost. A tree can contain multiple processes, completion order can change, and throughput is not guaranteed.

Each owner has an independent maintainer runtime/session. Programmatic `Options.Maintainer` is a singleton requiring capacity one; `RuntimeFactory` must create a fresh agent per owner. The scheduler, Drain/RunOnce, direct hierarchy, and optional model-lint share capacity. Direct hierarchy/model-lint admission includes waiting within a total five-minute bound, shortened by caller deadlines and read limits. Concurrent Drain calls wait cancelably and report only their own claims. Wake hints are bounded and lossy; durable operations and periodic scans recover missed hints. Admission precedes claims.

Inference occurs outside the canonical write lock. One publication gate orders canonical commit, full-snapshot projection, and durable outcome across maintenance, hierarchy, and source synchronization. Catalog/log preconditions can conflict even for different factual pages. Stale plans fail permanently with class `canonical_conflict` and reason `precondition_failed`, preserving the first commit. There is no automatic rebase, replanning, or correction turn; review current content before explicit retry.

Stop closes admission and new claims, then drains within the active shutdown caller's bound. Expiry cancels execution and returns the context error. Concurrent Stop callers honor their own deadlines without interrupting the first graceful drain. Live resources remain retained; subsequent Stop joins exited owners and retries failed cleanup before closing storage. Providers must cooperate with cancellation; live resources are not force-closed.

Stop all writers before standalone projection rebuilds, migrations, or backups. To return to one owner, stop/drain, set `workers: 1`, and restart while preserving workspace, immutable raw history, database, and journals.

## Container Operations

- **Startup**: The container runs `knowl --config-dir /etc init`, then `knowl --config-dir /etc start`, initializing a valid first-boot workspace and binding `0.0.0.0:8080`.
- **Mounts**: Persist all `/var/lib/knowl` so raw history, source status, tombstones, journals, and SQLite survive restarts. Host applications must not be mounted into the canonical workspace or mutate `wiki/**`. Mount sources separately read-only, such as `-v /path/to/source:/sources/source:ro`; the engineering example uses `/sources/engineering`.
- **Build**: Root `Dockerfile` accepts `VERSION`, `REVISION`, and `CREATED`, runs non-root, and emits OCI metadata. `scripts/smoke-test-sidecar.sh` verifies persistence/authentication.
- **Launch**: Direct port publication uses `-p 127.0.0.1:8080:8080`. Compose assets are `deploy/sidecar/compose.yaml`. `KNOWL_IMAGE` changes the image name; the service quickstart still builds the current checkout. A prebuilt deployment needs an image containing the required features, pinned by immutable digest.
- **Profiles**: `deploy/sidecar/quickstart.yaml` is complete configuration and forwards `OPENAI_API_KEY`, `OPENAI_MODEL`, and `KNOWL_OPERATOR_TOKEN`. Historical `quickstart.compose.yaml` is pinned to v0.5.0 and must not be combined with current optional-feature profiles.
- **Source status**: Inspect with `docker compose -f deploy/sidecar/compose.yaml exec knowl knowl --config-dir /etc source status <source-id>`. Each source explicitly configures `sync.on_start`; failed sync does not discard prior snapshots or make the service unready.
- **Web access**: Current images bundle Go templates/local assets without Node/CDN dependencies. Container overlays use `web.enabled: true`, `server.listen_addr: 0.0.0.0:8080`, and `operator.token: ${KNOWL_OPERATOR_TOKEN}`. Explicitly forward secrets or mount local overrides under `/etc/knowl/`.

### Build, Process, and Retrieve

The current-checkout service quickstart requires Git, Docker Compose, curl, an OpenAI API key, and a model available to that key. It uses the existing OpenAI maintainer provider, so the base container needs no additional ACP executable. To use an ACP maintainer, supply its executable and authentication inside the container or run Knowl in the agent's environment. The optional embedding API remains a separate dependency.

From the repository checkout:

```bash
export OPENAI_API_KEY='your-api-key'
export OPENAI_MODEL='a-model-available-to-your-account'
export KNOWL_OPERATOR_TOKEN='replace-with-a-local-secret'

docker compose -f deploy/sidecar/compose.yaml up --build -d --wait
curl -fsS http://127.0.0.1:8080/readyz
```

The service runs as a non-root user on container port 8080, published only to host loopback. The `knowl-data` named volume retains its workspace and SQLite state. The example engineering source synchronizes on startup and periodically. Inspect maintenance with:

```bash
docker compose -f deploy/sidecar/compose.yaml \
  exec knowl knowl --config-dir /etc source status engineering
```

Wait for committed maintenance and inspect failed operations. Scan success records accepted sources, and `/readyz` reports service readiness; neither establishes completion of model-backed processing.

Retrieve evidence with:

```bash
curl -fsS --get \
  -H "Authorization: Bearer ${KNOWL_OPERATOR_TOKEN}" \
  --data-urlencode 'query=Engineering shared page' \
  http://127.0.0.1:8080/v1/retrieve
```

A successful example returns a nonempty `evidence` array containing page identities, excerpts, and source references. Follow the returned page identity when inspecting the wiki in the volume or browser. Accepted revisions remain under `raw/`; the maintainer writes separate semantic pages under `wiki/`. Preserve both alongside operational state.

For browser access, copy the profile's complete configuration, enable `knowl.web.enabled: true`, and retain provider, sources, storage, server, and operator settings. Replace the existing configuration mount with that complete file at `/etc/knowl/config.yaml`, then recreate the Knowl container. The bundled UI uses the same listener and needs no additional web service.

Stop the stack with:

```bash
docker compose -f deploy/sidecar/compose.yaml down
```

This preserves named volumes. Retain the whole `/var/lib/knowl` volume. Lexical fallback may allow readiness while hybrid retrieval is degraded; inspect the retrieval response for effective mode.

## Configuration

The CLI defaults to `.config/knowl/config.yaml`. `--config-dir` selects an additional configuration root; `--profile` selects a top-level profile. `runtime:` contains the Balda-compatible typed provider registry; `knowl:` contains application settings.

| Setting under `knowl` | Contract |
| --- | --- |
| `provider` | Entry in `runtime.providers`; readiness requires configured or explicitly injected maintenance |
| `workers` | Integer `1` or `2`, default `1` |
| `output.max_corrections` | Integer `0` or `1`, default `1` |
| `workspace.path` | Defaults to `.` |
| `storage.type` | Defaults to `sqlite`; selects one optional typed storage block |
| `storage.sqlite.path` | Default `.knowl/knowl.sqlite` |
| `storage.postgres.dsn` | PostgreSQL DSN, commonly `${KNOWL_POSTGRES_DSN}` |
| `server.listen_addr` | Default `127.0.0.1:8080`; container may use another literal IP bind |
| `web.enabled` | Boolean, default `false` |
| `operator.token` | Nonempty value protects `/v1/*`, `/mcp`, and enabled operator data routes; required for web access |

Common overrides are `KNOWL_PROVIDER`, `KNOWL_WORKSPACE_PATH`, `KNOWL_STORAGE_TYPE`, `KNOWL_STORAGE_SQLITE_PATH`, `KNOWL_STORAGE_POSTGRES_DSN`, `KNOWL_SERVER_LISTEN_ADDR`, and `KNOWL_OPERATOR_TOKEN`. Keep tokenless deployments on trusted loopback boundaries. Secrets belong in the process environment or `.config/knowl/*.local.yaml`, not committed configuration.

Filesystem sources use `type: filesystem`, `filesystem.root`, `include`, and `flavor` (`obsidian`, `markdown`, or `okf`). Git sources use `type: git`, `git.remote`, `ref`, `ref_kind`, `include`, `flavor`, `uri_base`, authentication, and size limits. Both use `sync.on_start`, `interval`, `retry_initial`, and `retry_maximum`; documented examples use `on_start: false`, `interval: 5m`, `retry_initial: 1s`, and `retry_maximum: 1m`.

### Git Transfer, Identity, and Credentials

Git uses the same status, scheduling, retry, and maintenance lifecycle as filesystem sources. HTTPS/SSH scans pin one immutable commit. `auth.secret_env` supplies an HTTPS token or SSH private key; `auth.key_file` supplies an SSH key file. SSH also requires host-bound keys in `known_hosts`. Keep secrets out of `remote`, `uri_base`, and `repository_id`.

The default rejects non-fast-forward branches and moved tags while preserving the last successful checkpoint/catalog. `allow_rewrite: true` permits rewritten branch history. `rebind_ack: true` acknowledges an intentional new repository identity or moved tag for that synchronization; remove it afterward.

`<workspace>/.knowl/cache/git/<source-id>` is a rebuildable bare cache. Fetches target only the configured ref, retaining its complete history/objects without unrelated branches/tags. `include` filters paths after transfer and does not provide partial-clone or path-level network filtering. Remove caches only while Knowl is stopped. Older caches may retain previously mirrored objects until removed.

Azure DevOps SSH at `ssh.dev.azure.com` negotiates `multi_ack_detailed` and requests a complete pack for changed tracked refs, avoiding empty responses and unsupported incremental acknowledgement exchange. Unchanged refs skip pack transfer. Limits remain 500 MiB (`524288000`) per pack and 512 MiB (`536870912`) per source cache by default, configurable through `max_transfer_bytes` and `max_cache_bytes` up to 8 GiB.

Fallback `knowl://sources/...` provenance escapes Unicode, spaces, and invalid percent escapes. Replaying an accepted revision preserves its canonical provenance URI.

## Maintainer Providers and ACP

ACP connects Knowl to a wiki maintainer; MCP connects consumer agents to retrieval, ingest, and operation tools. Codex can fill either or both roles. `knowl.provider` selects the maintainer independently of consumer plugin installation.

- `codex_acp` launches the pinned bridge through `npx`, with `bridge_version: "1.7.3"`. Optional `model`, `reasoning_effort`, and `mode` use agent defaults when omitted. Node.js/npm and authenticated Codex must exist in Knowl's environment.
- `generic_acp` uses `cmd` as an argv prefix and appends `extra_args` without shell interpretation. Executables must implement ACP over stdin/stdout, required sessions, and structured plans, and be installed/authenticated where Knowl runs, including containers.
- The registry also supports named adapters such as Antigravity and hosted APIs such as OpenAI. Provider failures appear in maintenance status; raw sync success does not prove wiki completion. `knowl.embeddings` is separate and never replaces maintenance.

## Optional Web UI and Operator Reads

Enable with `knowl.web.enabled: true` and nonempty `knowl.operator.token`; embedded equivalents are `Config.Web.Enabled` and `Config.OperatorToken`. Startup rejects enabled web access without a token. MCP stdio disables web access. Disabled web routes return 404.

Open `http://127.0.0.1:8080/ui/` and enter the token through **Connect**. Public shell/assets contain no workspace data; `/ui/fragments/*` and `/operator/v1/*` require bearer headers. Browser tokens stay in document memory, never cookies, local/session storage, URLs, or exported JSON. Reload, **Disconnect**, and authorization failure clear the connection. Data responses use `Cache-Control: no-store`. The same token authorizes agent writes, including ingest; UI read-only behavior does not narrow credential authority.

Use loopback locally. Remote access needs a trusted HTTPS reverse proxy and restricted direct listener access; Knowl's listener serves HTTP and does not configure TLS certificates. Forward bearer headers without logging credentials/query strings. Browsing uses local assets without automatic upstream URL/image fetching. Deliberate **Open original** navigation does not forward the token.

| Operator route, all GET | Purpose |
| --- | --- |
| `/operator/v1/catalogs` | Direct children, optional `parent_id` |
| `/operator/v1/pages` | Current factual-page summaries |
| `/operator/v1/page?page_id=...` | Current canonical page |
| `/operator/v1/source-revision?source_ref=...` | Immutable accepted text |
| `/operator/v1/sources` | Configured sources and saved status |
| `/operator/v1/sources/{source_id}` | Status and document inventory |
| `/operator/v1/operations` | Stored operations, optional `status`/`source_id` |
| `/operator/v1/source-retirement?adapter=...&source_id=...` | Exact-owner retirement progress |

Lists default to 50 items and accept limits 1–100; Operations UI starts at 10. Continue with the same route/filter/limit using opaque cursors bounded to 8 KiB. Restart invalidates cursors; canonical page/catalog cursors also bind to the snapshot. Unknown or duplicate parameters, GET bodies, and query strings over 16 KiB are rejected. Callers cannot select another scope/workspace. Unavailable saved sources fail without upstream substitution.

The four screens provide:

- **Knowledge**: Actual root/index content, verified catalog links/breadcrumbs, **All pages** directory tree with lazy children and bounded Back-restored state, **Details** metadata, and **Page sources** / **Read saved source**. **Open original** requires allowed credential-free HTTP/HTTPS provenance.
- **Search**: One retrieval per explicit submission, original server-ordered snippets and `lexical`/`hybrid`/`degraded`/`failed` badges. **View JSON**/**Export JSON** omit credentials; Back restores without another retrieval.
- **Operations**: Status/source filters, retries, retrieval/context budgets, plan/correction reports. Visible tabs poll queued/running work; hidden tabs pause and terminal status stops polling.
- **Sources**: Upstream heads, accepted/processing revisions, operation links, and history.

Sync, accepted evidence, and completed processing are distinct. Failed later scans preserve accepted revisions and last successful sync. Only a stored reconciled tombstone confirms document deletion. Selected context describes what maintenance read, not changed pages; missing historical reports mean unavailable rather than zero work or retrieval failure.

## Optional Embeddings and Hybrid Retrieval

Embeddings are independent of maintainer and deployment. `knowl.embeddings` accepts `enabled`, OpenAI-compatible `endpoint` (full URL, at most 2,048 UTF-8 bytes, without userinfo/fragments), `model`, immutable `revision`, `dimensions` (1–4,096), optional `api_key_env`, prefixes, and `failure_policy` (`lexical` or `strict`). Model/revision/prefix strings are bounded to 256 UTF-8 bytes; empty prefixes work. Missing configured credential environment variables fail startup. Standard TLS supports HTTPS; private HTTP works. Redirects and inference retries are disabled. Disabled embeddings create no client, read no credentials, and make no requests.

Requests permit 16 inputs, each 2,048 UTF-8 bytes including prefix, a 64 KiB body, four concurrent calls, and a 10-second or earlier caller deadline. Responses are capped at 1 MiB and require complete unique indices, matching model/dimensions, and finite nonzero normalized vectors.

Semantic preparation retains NFC/original case, covers complete published pages within bounds, and uses up to four windows for queries/signals. Page inputs follow sections/block boundaries with at most 384 runes; query/signal windows overlap by 64 runes. This refines the older fixed-overlap page description; see [Content and Trust Boundaries](content-and-trust-boundaries.md) for the current native projection contract.

Projection limits are 8,192 chunks, 1 MiB coverage metadata, 64 MiB per scope, and a 15-minute rebuild deadline. Rebuild replaces lexical state first and performs inference in batches up to 16 without SQL locks; concurrent snapshot changes discard stale builds. Embedded applications can use `knowl.RebuildProjection(ctx, config, snapshot)`. Startup retries projections once; queries never rebuild or download models.

Public HTTP/MCP expose effective mode/safe reason, such as `{"retrieval":{"effective":"degraded","reason":"unavailable"}}`. Go `QueryResult.Retrieval`, `IngestResult.Retrieval`, and durable `Operation.Retrieval` contain detailed bounded reports with model-space prefix, candidate/scanned counts, and omitted coverage, excluding text, endpoints, credentials, and error bodies. Migration 16 introduced vector state/reports; migration 21 expanded ordinal ranges/coverage manifests.

The CPU reference pairs base Compose with `deploy/sidecar/embeddings.compose.yaml` and complete replacement configuration `deploy/sidecar/embeddings.yaml`, retaining OpenAI maintenance, engineering source, storage, and token without key-wise merging. It pins `linux/amd64` CPU TEI 1.9.0 and E5-base revision `d128750597153bb5987e10b1c3493a34e5a4502a`, yielding 768 normalized dimensions and persistent `tei-model-cache`. Resources are 2 CPU cores/4 GiB with ten-minute cold-start allowance. Observations were 194.5s cold readiness, 6.6s cached restart, 1,075 MiB cache, and about 1.91 GiB idle memory. Project-local TEI runs separately on host loopback with matching endpoint/model/revision/dimensions/prefixes.

## Operator CLI and Workspace Adoption

Core commands include `knowl init`, `knowl validate`, `knowl start`, and `knowl retrieve '<query>'`. `retrieve`, `ingest`, and `operation` are one-shot wrappers over service semantics. Source management is operator-only and has no MCP tool.

Bootstrap commands are `knowl bootstrap wiki <path>`, `knowl bootstrap obsidian <path>`, and `knowl bootstrap okf <path>`. They retain fresh-workspace guards and perform exactly one shared sync with source IDs `bootstrap-wiki`, `bootstrap-obsidian`, or `bootstrap-okf`. OKF flavor preserves v0.2 metadata, controls, Unicode paths, and standard links; absent version means v0.2, other declared versions are consumed best-effort with `diagnostics`. Generated configuration includes a provider and retains the source. Existing operator configuration is not rewritten; add its source entry for ongoing use. Ordinary sync accepts existing workspaces, saves evidence in `raw/`, and queues semantic maintenance without wiki mirrors. Bootstrap is optional.

Legacy conversion uses explicit idempotent `knowl migrate okf-v0.2` after stopping writers and backing up. Startup, retrieval, and source status do not migrate. Validate and inspect retrieval before retiring backups. Hierarchy-store migrations preserve existing source operations, descriptors, leases, and statuses. Older binaries are safe only before any hierarchy row exists; afterward binary rollback requires restoring the pre-upgrade operational database, while canonical Markdown remains portable.

Source controls construct an in-process Host without HTTP or scheduled runners:

```bash
knowl source list
knowl source sync engineering
knowl source sync --all
knowl source status engineering
knowl source retry engineering --failure-class provider --dry-run
```

`knowl run` synchronizes, drains maintenance, and reconciles hierarchy without a daemon; options include `--source <id>`, `--no-sync`, and `--no-hierarchy`.

`on_start` attempts are asynchronous. Each source has independent interval/capped retry state; one source cannot overlap itself, while different sources progress independently. Failures preserve readiness and successful retrieval snapshots. Restart recovers staged source work before readiness.

## Source Retirement Status and Recovery

Owners use exact adapter/source ID identities. Removal marks an owner `retiring`; simultaneous additions must each finish accepted sync and durable maintenance-readiness before cleanup. This ordering creates no alias or replacement mapping. Pure removal needs no addition gate; document tombstones do not retire configured owners.

Inspect with `knowl source status <source-id> --retirement-adapter <adapter>` or the adapter-qualified operator route. Ordinary source status remains the sync/document view. Retirement counts bounded page units under `operations.pending`, `planned`, `applying`, `projection_pending`, `committed`, and `failed`. Discovery reports cursor, EOF, round matches, and verified remaining refs. `deleted` requires no pending/applying/projection publication and a complete canonical zero-ref verification round.

Blockers distinguish addition readiness, remaining refs, pending work, and failed work. Bounded diagnostic samples retain paths/targets. `plan.empty_safe_subset` remains the summary while `citation.plan_missing`, `citation.unknown_source`, `citation.lineage_removed`, `citation.source_retiring`, and `catalog.dependency_rejected` remain attached.

Publication uses current source authority for ordinary maintenance and provenance-bearing catalogs. Retiring sources reject queued stages; reconfiguration/reactivation can reject an old stage with `citation.source_authority_changed`. Unrelated source changes do not invalidate current stages. Committed journals/projections replay even after later authority changes.

Preview with `knowl source retry <source-id> --retirement-adapter <adapter> [--page-id <path>] --dry-run`; omit `--dry-run` to requeue. Without `--page-id`, retry targets all failed page operations for that owner, never completed ones. Restart resumes durable pending work and recovery without manual SQL/wiki edits.

Per discovery call: at most 32 affected pages and 1,024 examined canonical entries; a round has a 100,000-page ceiling. Page plans retain 32-file, 256 KiB/file, and 64-ref limits. Active retirement evidence is capped at 64 refs/4 MiB. Restoring the exact source fences new claims by lifecycle generation without globally rolling back reconciled pages. Prefer roll forward; older binaries lack retiring-ref authority enforcement. One Host/coordinator supports each writer set; shared multiprocess filesystem writers require external serialization.

## Explicit Hierarchy Operations

Stop competing writers, then run `knowl hierarchy reconcile` and `knowl validate`. Reconciliation reserves and claims exactly its hierarchy operation without HTTP, general scheduler, source jobs, or `on_start` sync. Structured JSON reports generation and changed catalog/log files; converged replay returns `"changed":false` without canonical digest change.

Identity includes `hierarchy-v3` and effective output policy. Taxonomy uses deterministic bounded page metadata, excerpts, and current memberships. Schema digest binds policy identity, but schema content, raw bodies, provenance, and source-native paths are not taxonomy input. Subject grouping is recursive; type/technology are supporting signals, and secondary memberships are sparse. Semantic quality remains provider-dependent.

Only hierarchy controls under `wiki/index.md` and `wiki/catalogs/**/index.md` change; factual pages/raw evidence remain byte-for-byte. Limits are 1,024 pages/catalogs/edits, 16,384 edges, depth 16, 4 MiB input, 4,096 excerpt characters/page, 1 MiB output/manifest, and 256 KiB/catalog. Stale snapshots, unsafe/incomplete graphs, empty non-root catalogs, or exceeded bounds fail closed. Empty root is valid only for an empty wiki. Startup/source synchronization do not independently reconcile hierarchy.

## Source Maintenance Budgets and Signals

Default document reads allow 262,144 characters and 4 MiB under read deadlines. Output-affecting rules participate in maintenance generation; changed policy can reconsider an unchanged document once.

Source-maintenance-v6 separates full catalog graph from factual reads, defaulting to 20 ordinary candidates. Catalog defaults are:

| `types.CatalogLimits` Go field / JSON field | Default |
| --- | --- |
| `MaxCatalogs` / `max_catalogs` | 1,024 including root |
| `MaxEdges` / `max_edges` | 16,384 unique internal children summed by catalog |
| `MaxDepth` / `max_depth` | 16 including root |
| `MaxPathBytes` / `max_path_bytes` | 2,048 |
| `MaxCatalogBytes` / `max_catalog_bytes` | 262,144 per original/rendered catalog |
| `MaxSnapshotBytes` / `max_snapshot_bytes` | 4,194,304 combined catalog Markdown |
| `MaxInputBytes` / `max_input_bytes` | 4,194,304 serialized graph |

Catalog input is complete or rejected, never truncated. Additions and factual edits share 32 files/256 KiB per file. Embedded `Config.IngestOptions.CatalogLimits` accepts all-zero defaults or all seven positive fields; partial custom values are invalid and path bytes cannot exceed 2,048. CLI YAML exposes neither `knowl.ingest` nor `knowl.maintenance`. Existing catalogs require original digests/no title; new catalogs require titles/children. Duplicate memberships are no-ops. Use `catalog_additions`, never raw catalog FileEdits.

The complete current source prompt defaults to at most 4,194,304 UTF-8 bytes, including instructions/schemas/framing, escaped JSON, base64 schema content, source, catalog graph, and retained factual metadata/provenance. It is not a token, session-history, backend HTTP, or RAM guarantee. Embedded `MaintenanceInputLimits.MaxRequestBytes` accepts zero defaults or positive values up to 4,194,304; effective cap is the smaller application/provider limit. CLI has no YAML budget option.

The runtime reserves 128 correction bytes even with correction disabled. Initial `used_bytes` excludes that reserve. Indispensable overflow fails permanently as class `input_budget`, reason `required_input_limit`, before factual reads/inference/staging, retaining raw bytes. Nothing is silently truncated. Whole candidates are considered in priority order; controls/duplicates do not consume allowance, omissions do, and a large omission does not block later smaller pages. Read/parse failures remain errors. Wire `Content` occurs once; `Body` stays application-side. Replacements require retained full snapshots and exact digests, including `FilePlan`. Omission can reduce duplicate/contradiction detection.

`app.MaintenanceRequestSizer.RequestBudget()` declares positive `MaxBytes`, bounded `ReservedBytes`, and printable nonsecret `FormatVersion` up to 256 UTF-8 bytes, captured at construction. Invalid declarations reject before acceptance/reservation. `RequestBytes(ctx, input)` measures without runtime creation/download/inference. Plain custom maintainers assume `app.EncodeSourceMaintenanceRequest`; wrappers must size their complete framing. Actual built-in overflow fails safely as `provider_input_limit`. Transient `IngestResult.Budget` records measured `MaxBytes`, `UsedBytes`, `IncludedCount`, and `OmittedCount`; replay need not reconstruct it.

### Source Signal Parsing

Valid leading `---` YAML blocks support LF/CRLF, string `title`, and string-sequence `tags`. Other keys are ignored after structural validation. Invalid YAML, duplicate/nonstring keys, aliases/merges, extra documents, invalid field types, or exceeded metadata bounds discard all metadata signals. Closed invalid frontmatter stays excluded from prose; unterminated frontmatter is ordinary Markdown except delimiter/rule lines. Invalid UTF-8 fails as `source_signals`, preserving raw evidence.

Fallback title uses first eligible ATX heading (`#`–`######` followed by ASCII space/tab), Setext heading, then prose. Backtick/tilde fences of at least three markers at up to three ASCII spaces suppress code until a matching adequate closer; indented code uses four-column tab stops. The parser does not implement full CommonMark, HTML interpretation, or inline rendering.

Metadata limits are 262,144 bytes, 16,384 nodes, depth 64. Each title/tag/heading allows 256 runes; first 32 tags/headings are examined. First-seen spelling/order is retained and tags/headings deduplicated. Combined clipping uses title, tags, headings, body priority within 4,096 runes/16,384 UTF-8 bytes. The body is an eligible prose prefix. Lexical queries permit 32 distinct terms/256 normalized runes. These fixed limits have no YAML settings; full accepted `SourceText` remains available to maintenance. Custom indexes must consume `SourceSummary.Tags`, `Headings`, and `Body`; slice fields make the Go struct non-comparable with `==`.

### Direct Literal Index Bounds

Generic NFC/lowercase/NFC matching preserves accents and original snippet spans, without stemming, full case folding, transliteration, or language routes. English/Russian casing and canonical accents match; `what`/`why` are normal words. AND matches precede deduplicated OR fillers, semantic field priority is title/tags/description/body, and path ties are deterministic. Backend native scores/position limits may differ.

| Input/result | Bound |
| --- | --- |
| Raw direct-index query | Valid UTF-8, 65,536 bytes |
| Examined raw signals | Valid UTF-8, 65,536 combined bytes; first 32 tags/headings |
| Original semantic field | Valid UTF-8, 4,194,304 bytes each |
| Derived token streams | 524,288 combined bytes; 8,192 distinct supported words |
| Queryable word | 256 normalized runes; longer words excluded whole |
| Source filters | 256 raw entries, 16 distinct validated IDs, sorted/deduplicated |
| Direct snippet limit | Nonpositive defaults to 4,096 runes; positive clamps at 262,144 |

Invalid queries/signals/projections return stable typed errors; adapter `ErrInvalidProjection` aliases the shared sentinel. Dense pages can exceed derived limits despite fitting file limits. Failed rebuilds preserve prior scoped index/readiness atomically without truncating supported words. `QueryService` retains its positive read defaults.

Migration `00015_generic_lexical` clears derived tokens/readiness while preserving original pages, provenance, links, operations, source state, and canonical/raw files. PostgreSQL reuses GIN with explicit vectors and no encoded-text columns. Startup rebuilds before readiness. Down invalidates readiness; PostgreSQL restores generated A/B/C/D vectors, SQLite empty FTS. Pair binary/schema/policy and rebuild with writers stopped.

## Maintenance Scheduling, Correction, and Recovery

Source status includes counts/samples for `queued`, `retrying`, `replayed`, `committed`, and `failed`, correlating documents/revisions with operation IDs. Safe fields include `generation_prefix`, `work_attempt`, `retry_attempt`, `manual_retry_count`, failure class/reason, and `next_retry_at`; raw text, prompts, provider errors/output, and credentials are excluded.

`queued` includes ready/claimed nonterminal work. `retrying` is unleased future-eligible work. `replayed` means more than one durable attempt and can overlap outcomes. `failed` remains terminal until explicit recovery.

Scheduler retry cycles permit three total work attempts. First delay is at least 30 seconds, later exponential delays include deterministic bounded positive jitter, capped at five minutes. Counters/deadlines survive restart. Provider build/transport/execution failures are retryable; exhausted output remains terminal. Correction stays inside one attempt and does not increment scheduler retry counts.

`knowl.output.max_corrections` accepts integer 0/1, default 1, across source/hierarchy. It permits at most one complete replacement for malformed JSON, schema/branch rejection, or application-plan rejection, using original captured input and one safe code: `structured_output_invalid`, `source_plan_invalid`, or `hierarchy_plan_invalid`. Every candidate passes full validation; existing safe-subset warnings remain supported.

Turns share 1,048,576 output bytes and five minutes including runtime wait/setup. Accounting charges collector partial/final text, excludes thoughts, and reports accepted prefixes on overflow. Earlier caller stops win. Input/setup/transport failure, overflow, timeout, cancellation, and canonical conflicts do not trigger correction. Permanent reasons are `provider_output_exhausted`, `provider_output_limit`, and `provider_output_deadline`; earlier caller stops retain existing recoverability. Embedded configuration uses `Config.Output`/`types.OutputSettings`. Base custom maintainers remain single-call without validating capabilities; `FilePlan` invokes no correction.

### Policy Changes and Failed Work

Generation binds source contract, schema, request cap/format, read/plan/catalog limits, embedding/chunk/fusion/candidate/capacity/failure policy, and effective correction support/limits/reservation. Endpoint, credentials, CPU/runtime settings, and measured usage are excluded. Unplanned old/empty-generation work fails before embeddings/inference with class `maintenance_policy`, reason `maintenance_policy_mismatch`; restart does not reinterpret descriptors.

Authenticated concrete older stages, including v1 catalog FileEdits and hierarchy stages, recover without inference under original schema/provenance/digest preconditions. Their projection replay publishes lexical state; dense repair is separate. Terminal replay returns historical outcomes. Corrupt/stale stages still fail normally.

Configured-source recovery uses:

```bash
knowl source status engineering
knowl source retry engineering --failure-class maintenance_policy --dry-run
knowl source retry engineering --failure-class maintenance_policy
```

Public ingest resubmits identical immutable input/origin/idempotency key under current generation; historical operations/raw remain preserved. Changed content requires a new idempotency key. Embedded callers can reserve accepted sources through the existing seam. Do not rewrite descriptors or delete raw data. Older binaries without guards must not process newer queued work.

Complete scans compare stored/current policy for unchanged documents. Older committed operations or terminal `source` failures can receive one current-generation operation; `provider`, `staging`, unknown/unrecognized failures keep manual gates. Repeated same-generation sync converges. Logs use `maintenance_trigger` (`revision`, `policy`, `unchanged`), `maintenance_outcome` (`queued`, `replayed`, `converged`, `manual_gate`), and 16-character `maintenance_generation`, without secret/text payloads.

Start class-filtered manual recovery with `knowl source retry <id> --failure-class <class> --dry-run`; remove the flag after fixing the cause. Results contain `source_id`, `dry_run`, `matched`, `requeued`, `rejected`, `operation_ids`, and `truncated`. Counts cover all matches; IDs cap at 100. Preview changes no state and reports zero requeued.

Current-generation retries happen in place, preserving total attempts. Older failures create distinct current-generation operations and retain history. Both start fresh bounded retry cycles, increment `manual_retry_count`, and wake scheduling without source sync. Any committed/stale/cross-scope/non-source-maintenance/leased selected candidate rejects the entire request atomically. Repeating a successful retry matches no terminal work. Fix provider failures first; select additional classes only after separate diagnosis. Preserve additive generation state and raw/history on rollback.

### Safe Subsets and Original Links

Committed diagnostics expose bounded stable `code`, canonical `path`, and optional normalized `target`. Unresolved links are preserved only when proven in accepted source text, reported as `link.original_unresolved`; invented unresolved links remain invalid. Invalid provenance withholds that page and its dependent catalog closure while safe siblings may commit/index. Empty safe subsets and plan-wide schema/path/bound/graph failures publish nothing.

## HTTP/MCP Contract and Operation Inspection

The three business routes are `GET /v1/retrieve?query=...`, `POST /v1/ingest`, and `GET /v1/operations/{operation_id}`; MCP uses the matching three tools. Public states are `queued -> running -> completed | failed`, with host-owned scope. Public ingest accepts exactly one trimmed nonempty `content` or `uri`. Text defaults to `text/plain`; URI references default to `text/uri-list` and are stored without downloading or redirects. `origin` is an identity hint, not fetch/citation URI metadata. Poll the returned operation ID; MCP operation arguments use `{"id":"<operation_id>"}`.

HTTP and MCP expose matching optional `details` alongside existing `id`, `status`, `updated_at`, `failure`, and retrieval summaries. Stable classified `failure.reason` contains no provider error text.

| Detail | Interpretation |
| --- | --- |
| `context.work_attempt`, `outcome` | Producing attempt; `assembled`, `selection_failed`, or `assembly_failed` |
| `context.candidate_count`, `catalog_count` | Eligible unique ordinary candidates within allowance and observed catalog count |
| `context.pages` | IDs, first reason (`lexical`, `vector`, `hybrid`, `neighbor`, `recent`, `unknown`), disposition (`included`, `budget_omitted`, `pending`) |
| `context.entries_omitted` | Reporting omissions, not removals from model input |
| `context.budget` | Maximum, known used bytes, included/omitted counts; pending is not omitted |
| `context.vector_projection` | Same-attempt `not_checked`, `ready`, or `invalid`, with safe reason |
| `retrieval`, `retrieval_attempt` | Requested/effective modes, safe reason, coverage/counts, fingerprint, producing attempt |
| `correction` | Producing attempt, allowance, byte/deadline limits, outcome, known turns/corrections/prefix bytes/code |
| `plan` | Validated/staged SHA-256 digest and known file count, without edits/rationale |
| `warnings`, `warnings_omitted` | Bounded codes/identifiers and omitted count |
| `execution` | Work/retry/manual/apply counters and optional `ready_at`; apply is not inference count |

Context finalizes once per attempt before inference or a measured selection/assembly failure. `assembled` does not imply successful inference/commit. Mid-fit failures retain measured accepted prefixes with remaining entries pending. Required-input overflow may exceed reported cap; unknown exact sizing omits used bytes. Missing values represent unavailable old/custom/preselection/crash evidence. Historical context/retrieval attempts can differ from current execution. Polling/replay never reconstructs reports or invokes selection/models.

Correction outcomes are `accepted`, `exhausted`, `output_limit`, `deadline`, `canceled`, `provider_failed`, and `unavailable`. Turns count actual inner calls, corrections later started calls, bytes accepted collector prefixes. Setup can report zero turns; unmeasured custom adapters omit counters. Reports finalize before staging/failure with a five-second durable write timeout. Accepted-report persistence failure stops staging; rejected-report failure preserves the original cause.

Context caps are 32 KiB/100 candidates/2,048-byte identifiers; retrieval 2 KiB; warnings 64 entries/32 KiB; correction 1 KiB; total serialized details below 96 KiB. Authorized callers can see page names, never source/query/prompt text, edits/rationale, provider messages, credentials, or endpoints. Preserve SQLite/PostgreSQL databases for historical diagnostics. Migration 17 adds nullable context/file counts; migration 18 adds nullable correction without backfill. Downgrade drops only those observations, preserving canonical/raw/stages and existing fields. Legacy opaque digests have no public SHA-256 plan summary.

## Local Codex Plugin

The consumer workflow requires published v0.7.1, Node.js/npm/npx, Codex CLI, and the configured maintainer (default authenticated `opencode acp`). Run `npx --yes @baldaworks/knowl@0.7.1 setup codex` at project root. Setup creates missing `.config/knowl`, `schema.md`, `raw/`, `wiki/`, `.knowl/`, validates, and installs release-matched marketplace/plugin while preserving existing files. Conflicts need explicit approval and rerun with `--replace`.

Start a new thread. Exactly two skills exist: `$knowl:setup` repeats/repairs setup; `$knowl:run` validates, runs one bounded cycle, validates, and reports structured changes under Knowl-owned paths. Local binaries require exact `knowl version --json` identity `0.7.1`; otherwise the entire workflow uses pinned `npx --yes @baldaworks/knowl@0.7.1`. Plugin MCP stdio needs no token, port, daemon, or `codex mcp add`. This supersedes the prior v0.6.1 recipe without changing historical release facts.

Native packages cover macOS/Linux x64/arm64 and Windows x64. Prewarm exact npm artifacts and pinned Git marketplace online. Sparse checkout limits materialized plugin paths, not all Git metadata transfer. Supported scope is local Codex projects/local filesystems; hosted Codex, remote MCP, network/shared filesystems, and secondary folders remain outside verified scope. Separate projects isolate state; threads in one project coordinate shared durable storage/workspace. Unrelated user changes remain outside Knowl writes.

## Testing and Quality Verification

- Native tests: `go test ./...`
- Lint: `go tool golangci-lint run ./...`
- Architecture: `go run github.com/fe3dback/go-arch-lint@v1.15.0 check --project-path .`
- PostgreSQL: `go test -tags integration -count=1 ./pkg/knowl/store/postgres -run TestStoreContractWithTestcontainers`
- Context baseline: `go test -count=1 -json ./pkg/knowl/... -run 'TestContextBaseline|TestIngestRejectsStaleReviewedPlan' > /tmp/knowl-context-baseline.jsonl`, measuring retrieval, signals, catalogs up to 32, 4 MiB fitting, and concurrency.
- CPU embeddings: `go test -tags integration -count=1 -v ./pkg/knowl/store/eval -run 'TestQualityCorpus|TestRealCPUModel'` with `KNOWL_EMBEDDING_EVAL_ENDPOINT`, `KNOWL_EMBEDDING_EVAL_POSTGRES_DSN`, and `KNOWL_EMBEDDING_EVAL_OUTPUT`. Pinned multilingual E5-base revision `d128750597153bb5987e10b1c3493a34e5a4502a`, 768 dimensions, passed 13/13 top-five query/source cases and 12/12 golden corpus across stores.
- Ownership gates: `go test -count=2 -json ./pkg/knowl ./pkg/knowl/app -run 'TestContextBaselineBlockedExecution|TestHostComposesDistinctRuntimeSessions|TestConcurrentPreparedSourcePlansFailSafely|TestOverlappingSourceAndHierarchyPreserveFirstCommit'`, covering serial capacity one, parallel capacity two, and preserved typed conflicts without automatic replanning.

See [Architecture](architecture.md), [Public Contract](public-contract.md), [Execution Modes](execution-modes.md), and [Content and Trust Boundaries](content-and-trust-boundaries.md) for related design, usage, and integrity contracts.
