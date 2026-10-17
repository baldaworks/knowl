---
type: concept
title: Service Configuration, HTTP/MCP Transports, and Operator API
knowl:
  id: concepts/service-configuration-and-transports
  source_refs:
    - wiki-filesystem:knowl-docs/operations.md@ae3c7b6ee1583fd88681e5a3ee4bb1cdf2a9b007235663169cf7e59a41b60baa
    - wiki-filesystem:knowl-docs/postgres.md@70888829c1127d091a9c411bbe062a67d3e9da37094408df577821faf8c5c656
    - wiki-filesystem:knowl-docs/service.md@646948819ed449ec0af05909f13981d098c69e75cd4e7c9b09749f4c76799754
    - wiki-filesystem:knowl-docs/sidecar.md@a49cb8614961df7fbb475965018e0531ca85dca60964863716d383e52b526f78
    - wiki-filesystem:knowl-docs/web-ui.md@fdf8776452fa7a187b2ab02c1f9c637d49472fa460390a57f0da9a30de5e3a41
---
# Service Configuration, HTTP/MCP Transports, and Operator API

Knowl uses a shared configuration and runtime model across project-local CLI execution, persistent MCP/HTTP service deployments, and embedded Go applications. Clients connect over MCP or HTTP while the optional browser UI shares the same listener, and the wiki maintainer and optional embedding API are configured as separate dependencies. See [Knowl Overview and Operating Modes](knowl-overview.md) and [Agent Maintainers and Provider Configuration](agent-maintainers.md).

## Configuration Files, Profiles, and Environment Overrides

The CLI loads `.config/knowl/config.yaml` by default. `--config-dir` specifies an additional configuration root and `--profile` selects a top-level profile. Configuration is divided into two top-level sections:

- `runtime`: Shared provider registry in Balda-compatible typed shape.
- `knowl`: Knowl application, workspace, storage, server, web, operator, and source settings.

### Storage and Server Defaults

- **Storage (`knowl.storage.type`)**: Defaults to `sqlite` (e.g., `.knowl/knowl.sqlite`) when omitted. PostgreSQL is selected with `type: postgres` and `postgres.dsn` (for example, `'postgres://KNOWL_RUNTIME@DB_HOST/KNOWL_DB?sslmode=verify-full'` or `${KNOWL_POSTGRES_DSN}`; note that the configuration schema defines `dsn`, not `dsn_env`, while embedding credentials use `knowl.embeddings.api_key_env`). Keep real credentials and local overrides in `.config/knowl/*.local.yaml` or environment variables out of source control. See [Retrieval and Hybrid Search Architecture](retrieval-and-search.md) for PostgreSQL Goose migration streams and `pgvector` provisioning.
- **Listen Address (`knowl.server.listen_addr`)**: Defaults to `127.0.0.1:8080` for local use; container deployments typically bind `0.0.0.0:8080` inside the container and publish only to host loopback (`127.0.0.1:8080`) with workspace `/var/lib/knowl/knowledge`.
- **Authentication (`knowl.operator.token`)**: When non-empty, `/v1/*` and `/mcp` require an `Authorization: Bearer <token>` header. Health (`/healthz`) and readiness (`/readyz`) probes remain unauthenticated. Tokenless deployments must stay on a trusted, loopback-only network boundary.

### Environment Variable Overrides

Common `KNOWL_*` environment overrides include:

- `KNOWL_PROVIDER`
- `KNOWL_WORKSPACE_PATH`
- `KNOWL_STORAGE_TYPE`
- `KNOWL_STORAGE_SQLITE_PATH`
- `KNOWL_STORAGE_POSTGRES_DSN`
- `KNOWL_SERVER_LISTEN_ADDR`
- `KNOWL_OPERATOR_TOKEN`

## Host Startup Lifecycle, Readiness, and Sidecar Service Deployment

Host construction executes five ordered stages:

1. Workspace validation
2. Selected-store setup and migration
3. Durable recovery
4. Projection preparation
5. Listener startup

- `GET /healthz`: Indicates only that the process is serving HTTP.
- `GET /readyz`: Confirms the workspace is usable, the store is open, recovery completed, and projections are ready for retrieval and operation reads. Note that `/readyz` does not wait for asynchronous model-backed maintenance to complete, and lexical fallback can report ready while hybrid retrieval is `degraded` (inspect retrieval responses for the effective search mode).

In sidecar/service deployments, Knowl owns `/var/lib/knowl` (with canonical workspace `/var/lib/knowl/knowledge` and SQLite state under `.knowl/`), and the consumer agent communicates exclusively via MCP or HTTP without mutating `raw/`, `wiki/`, or `.knowl/` directly. For in-process Go applications, `pkg/knowl` provides the plain-Go runtime entrypoint and `pkg/knowlfx.NewApp` wraps the same runtime with Fx lifecycle management.

### Docker Compose Service Quickstart (`deploy/sidecar/compose.yaml`)

The repository keeps its Compose deployment assets under `deploy/sidecar/` for compatibility ("sidecar" describes the deployment arrangement rather than a separate Knowl mode, and the legacy `docs/sidecar.md` guide now redirects to `docs/service.md` and `docs/search.md`). The default Compose profile builds the current checkout and uses the built-in OpenAI maintainer provider so the container requires no additional ACP executable (to use Codex or another ACP agent instead, supply its executable and authentication inside the container or run Knowl directly in that agent's environment; see [Agent Maintainers and Provider Configuration](agent-maintainers.md)).

Prerequisites are Git, Docker Compose, `curl`, an OpenAI API key, and a model available to that account:

```bash
export OPENAI_API_KEY='your-api-key'
export OPENAI_MODEL='a-model-available-to-your-account'
export KNOWL_OPERATOR_TOKEN='replace-with-a-local-secret'

docker compose -f deploy/sidecar/compose.yaml up --build -d --wait
curl -fsS http://127.0.0.1:8080/readyz
```

- **Profile and Volume Layout**: Mounts `deploy/sidecar/quickstart.yaml` as the complete service configuration at `/etc/knowl/config.yaml` and forwards `OPENAI_API_KEY`, `OPENAI_MODEL`, and `KNOWL_OPERATOR_TOKEN`. The entrypoint initializes an empty volume and runs Knowl as a non-root user listening on port `8080` inside Docker (published only to host loopback). The `knowl-data` named volume preserves `/var/lib/knowl` (including `/var/lib/knowl/knowledge` with `raw/`, `wiki/`, and `.knowl/`), while the example source is mounted read-only at `/sources/engineering`.
- **Image Overrides and Historical Compose File**: Setting `KNOWL_IMAGE` changes the local image name while still building from source; for prebuilt deployments, use an image with the required features and pin its immutable digest (see [Release Lifecycle and Publishing](release-and-publishing.md)). The historical `quickstart.compose.yaml` is pinned to `v0.5.0` and must not be combined with current optional-feature profiles.
- **Inspecting Source Maintenance and Querying Evidence**: The configured source synchronizes on startup and periodically. Inspect committed maintenance or failures and query bounded evidence:
  ```bash
  docker compose -f deploy/sidecar/compose.yaml \
    exec knowl knowl --config-dir /etc source status engineering

  curl -fsS --get \
    -H "Authorization: Bearer ${KNOWL_OPERATOR_TOKEN}" \
    --data-urlencode 'query=Engineering shared page' \
    http://127.0.0.1:8080/v1/retrieve
  ```
  See [Maintenance Contract and Execution](maintenance-contract-and-execution.md) for recovering failed source maintenance and [Retrieval and Hybrid Search Architecture](retrieval-and-search.md) for the optional hybrid search Compose profile (`deploy/sidecar/embeddings.compose.yaml`), which is shared conceptually across local and service deployments.
- **Shutdown and Persistence**:
  ```bash
  docker compose -f deploy/sidecar/compose.yaml down
  ```
  `docker compose down` preserves named volumes; retain the entire `/var/lib/knowl` volume to preserve raw history, semantic wiki pages, source status, and recovery state (see [Workspace Semantics and Source Lifecycle](workspace-and-source-lifecycle.md)).

## Public HTTP and MCP Transports

Both HTTP (`api/openapi/knowl.yaml`) and MCP Streamable HTTP (`http://127.0.0.1:8080/mcp`) call the same underlying application services and expose the public operation state model `queued -> running -> completed | failed`. Callers cannot override the host-owned trusted scope.

| Operation | HTTP Route | MCP Tool |
| --- | --- | --- |
| Retrieve bounded evidence | `GET /v1/retrieve?query=...` | `knowl_retrieve` |
| Ingest text or URI reference | `POST /v1/ingest` | `knowl_ingest` |
| Read durable operation status | `GET /v1/operations/{operation_id}` | `knowl_operation` |

### Streamable HTTP MCP Client Configuration

Connect an MCP client to the running service using Streamable HTTP and the operator bearer token:

```json
{
  "transport": "streamable_http",
  "url": "http://127.0.0.1:8080/mcp",
  "headers": {"Authorization": "Bearer <operator-token>"}
}
```

For project-local MCP over `stdio`, see [Agent Maintainers and Provider Configuration](agent-maintainers.md).

### Ingest Payload Rules

`POST /v1/ingest` and `knowl_ingest` accept **exactly one** nonempty `content` or `uri` (supplying both or neither is invalid; surrounding whitespace is trimmed):

- **Text ingest (`content`)**: Defaults to `media_type: text/plain` (or `text/markdown` when specified). Accepts optional `origin` (a source identity hint, not a fetch instruction or structured citation URI) and `idempotency_key` (which must change when content changes).
- **URI reference (`uri`)**: Stores the URI string as the source body with default media type `text/uri-list` without downloading the page or following redirects.
- **Status polling**: Ingest returns a durable `operation_id`; callers poll `GET /v1/operations/{operation_id}` or `knowl_operation` (`{"id": "op_01K..."}`) until `completed` or `failed`.

## Optional Web UI and Operator Read API

The operator web UI (`http://127.0.0.1:8080/ui/`) and operator read API (`api/openapi/operator.yaml`) are bundled in the Knowl binary (requiring no separate web service, Node.js runtime, or frontend build tooling) and are disabled by default (`knowl.web.enabled: false`; in Go: `Config.Web.Enabled` and `Config.OperatorToken`; always disabled under MCP stdio).

To enable browser access in local or service configuration, merge this overlay into an existing valid configuration, set `KNOWL_OPERATOR_TOKEN`, and restart `knowl start`:

```yaml
knowl:
  web:
    enabled: true
  operator:
    token: ${KNOWL_OPERATOR_TOKEN}
```

In the sidecar Compose profile, copy the profile's complete configuration, add `knowl.web.enabled: true` while retaining `runtime.providers`, `sources`, `storage`, `server`, and `operator` settings, mount that complete file at `/etc/knowl/config.yaml` in place of the existing config mount, and recreate the container.

- **Authentication and Security**: Startup rejects `web.enabled: true` when `knowl.operator.token` is empty. Keep secrets in `.config/knowl/*.local.yaml` or environment variables out of Git. Open `http://127.0.0.1:8080/ui/`, enter the token, and select **Connect**. The public shell (`/ui/`) and bundled local assets contain no workspace data; HTML fragments under `/ui/fragments/*` and JSON reads under `/operator/v1/*` require the bearer token (returning `404` when disabled). The browser stores the token only in memory for the current document (preserved across in-app navigation, cleared on reload, **Disconnect**, or auth failure; never stored in cookies, `localStorage`, `sessionStorage`, URLs, or exported search JSON). All data responses set `Cache-Control: no-store`. Because the operator token also authorizes `POST /v1/ingest` and MCP tools, treat it as a write-capable credential. For remote access beyond loopback, terminate HTTPS at a trusted reverse proxy, restrict direct listener access, and keep tokens and query strings out of logs.

### Four Server-Rendered Browser Screens

1. **Knowledge**:
   - Opens the canonical wiki root and renders the actual `wiki/index.md` body. Breadcrumbs return through verified catalogs; direct links and reloads use the root and any verified current catalog rather than inferring deeper catalog ancestry from file paths.
   - **All pages** opens an expandable directory tree grouped by wiki paths, with **More files** when a branch has additional files. Each `index.md` appears as an ordinary file in this tree. Browser Back restores the loaded, expanded tree within the same connected tab while its bounded in-memory view is retained (cleared on reload or disconnect).
   - **Details** starts closed and shows page identity, digest/version, and available `type`, `description`, `tags`, `status`, `trust`, and `stale` metadata.
   - **Page sources** starts closed at every screen width and lists saved revisions supporting a leaf page (stating explicitly when a leaf has no references; catalog indexes have no source panel). Source references support the page as a whole rather than individual sentences.
   - **Read saved source** opens immutable accepted text with its digest and media type immediately after the source record without fetching upstream, focusing the loading state, accepted text, or error (reporting unavailable revisions explicitly). **Close saved source** clears the reader and returns focus to the button. **Open original** appears only when provenance contains an allowed, credential-free HTTP/HTTPS URL (local filesystem sources may have no original URL while their saved text remains readable). Normal browsing never automatically loads remote images or upstream content.
2. **Search**:
   - Submits a whole-wiki query across the existing retrieval path once when **Search** is selected (never while typing or merely opening the screen). A disclosure above results indicates whether embeddings are enabled (submitting an embedding-enabled query sends it to the configured embedding provider; browsing pages and saved sources does not).
   - Evidence stays in server order with original snippets and source references. The retrieval badge reports `lexical`, `hybrid`, `degraded`, or `failed` (or `unavailable` when diagnostics are missing), distinguishing a successful empty result from a failed retrieval.
   - **View JSON** and **Export JSON** expose the same safe response used for result cards without another retrieval or the operator token. Selecting an evidence title opens its current page (which may differ from the earlier search snapshot), and browser Back restores the single bounded in-memory query and results view without re-querying (oversized views cannot be restored; fresh Search views, reloads, or disconnects start blank).
3. **Operations**:
   - Filters stored processing history by status or source ID and displays full operation IDs in the list and expandable detail identity. **Back to operations** returns to the selected row or the list heading.
   - Details show execution/retry facts, retrieval and selected context reports (describing what the maintainer read rather than a changed-page list, and distinguishing candidates excluded by the request byte budget from entries omitted by reporting bounds; missing older reports are shown as unavailable rather than zero), a plan digest/count summary, and correction facts (separate from execution retries).
   - Selecting an operation reveals and focuses its details. A selected `queued` or `running` operation polls in the background while the tab is visible (keeping reading position and focus, pausing when hidden, backing off on transient failures, and stopping on terminal status, navigation, or disconnect). **Refresh** reloads the first list page while keeping active filters. The screen never starts work or retries failed operations.
4. **Sources**:
   - Selecting a configured source opens its saved documents first (distinguishing the upstream head, accepted revision, and processing revision, with **View operation** linking to the stored operation when available), followed by **Sync and processing** history below. Selecting a source or **Next documents** reveals and focuses the requested details beneath tall lists.
   - A failed sync preserves the last successful sync and accepted revisions. Only a saved reconciled tombstone confirms deletion (omission from a bounded page does not).

### Operator Read Routes

| Route (`GET`) | Purpose |
| --- | --- |
| `/operator/v1/catalogs` | Read direct catalog children (optional `parent_id`) |
| `/operator/v1/pages` | List current factual page summaries |
| `/operator/v1/page?page_id=...` | Read one current canonical page |
| `/operator/v1/source-revision?source_ref=...` | Read immutable accepted source text |
| `/operator/v1/sources` | List configured sources and saved status |
| `/operator/v1/sources/{source_id}` | Read source status and document inventory |
| `/operator/v1/operations` | List stored operations (optional `status` and `source_id`) |
| `/operator/v1/source-retirement?adapter=...&source_id=...` | Read typed retirement progress for one exact owner |

### Pagination and Request Guards

- List routes default to **50 items** (the Web UI starts general lists at **50 rows** and Operations at **10 rows**, using **Next** controls for continuation), accept `limit` from **1 through 100**, and return an optional opaque `next_cursor` bounded to **8 KiB**.
- Cursors require continuing with the same route, filter, and limit; they become invalid after a host restart or workspace change, and catalog/page continuations bind to the current canonical snapshot.
- Requests cannot override workspace or scope. Unknown or duplicate parameters, `GET` bodies, and query strings exceeding **16 KiB** are rejected.

### Source-to-Wiki Browser Walkthrough and Deterministic Showcase Fixture

To run a standalone local walkthrough in a temporary workspace using `examples/source-to-wiki/sources/authentication-service.md` (requires the Go version in `go.mod`, an OpenAI API key, and an available model):

```bash
KNOWL_UI_DEMO=$(mktemp -d)
go build -o "$KNOWL_UI_DEMO/knowl" ./cmd/knowl
mkdir -p "$KNOWL_UI_DEMO/sources" "$KNOWL_UI_DEMO/.config/knowl"
cp examples/source-to-wiki/sources/authentication-service.md \
  "$KNOWL_UI_DEMO/sources/"
export OPENAI_API_KEY='your-api-key'
export OPENAI_MODEL='a-model-available-to-your-account'
export KNOWL_OPERATOR_TOKEN='choose-a-local-secret'
```

Write `$KNOWL_UI_DEMO/.config/knowl/config.yaml` with `listen_addr: 127.0.0.1:8086`, `web.enabled: true`, `embeddings.enabled: false`, and filesystem source `engineering-docs` (`root: sources`, `include: ["authentication-service.md"]`, `flavor: markdown`), then run:

```bash
cd "$KNOWL_UI_DEMO"
./knowl init
./knowl run --source engineering-docs
./knowl validate
./knowl source status engineering-docs
./knowl start
```

Open `http://127.0.0.1:8086/ui/` to connect with `KNOWL_OPERATOR_TOKEN`, inspect `engineering-docs` in **Sources**, browse generated pages and **Read saved source** in **Knowledge**, query `session revocation JWT` in **Search** (and inspect **View JSON**), and follow **View operation** from **Sources**.

For reproducible offline verification of source acceptance, maintenance, and retrieval without a live model or browser (see [Testing, Baseline Gates, and Evaluation Harnesses](testing-and-evaluation-harnesses.md)):

```bash
go test -count=1 ./examples/source-to-wiki \
  -run '^TestSourceToWikiShowcaseEndToEnd$'
```
