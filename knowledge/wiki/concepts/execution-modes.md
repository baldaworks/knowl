---
type: topic
title: Execution Modes
knowl:
  id: concepts/execution-modes
  source_refs:
    - wiki-filesystem:knowl-docs/README.md@10d0fff017afc2a41b8399aacccd4b97683bf81e6b5575724f1af66b3eca9715
    - wiki-filesystem:knowl-docs/agents.md@e0b710a8a9c32653f3580c09abb08dbbe32b2322de428c63a60bae39370b377b
    - wiki-filesystem:knowl-docs/examples/own-docs-wiki.md@4b076ff94b528e7e337477c9190b8aa2b59649bb07e78bd65db880b1b28c8fa2
    - wiki-filesystem:knowl-docs/local.md@b1186f32503e604c07be2a94377577eb78e1a3b883ea074ec4d8415d0030abdc
    - wiki-filesystem:knowl-docs/service.md@646948819ed449ec0af05909f13981d098c69e75cd4e7c9b09749f4c76799754
    - wiki-filesystem:knowl-docs/web-ui.md@fdf8776452fa7a187b2ab02c1f9c637d49472fa460390a57f0da9a30de5e3a41
---
# Execution Modes

Knowl supports two operational execution modes that generate a Markdown knowledge base from configured sources and enable evidence retrieval: **Local mode** and **Service mode**.

The repository includes a working wiki generated from Knowl's own documentation, allowing comparison with the source documentation and inspection of saved provenance.

For overall system capabilities, interfaces, and package layering, see [Architecture](architecture.md).

## Local Mode
Local mode runs Knowl directly within a project repository using one-shot CLI commands and an ACP maintainer (such as Codex), eliminating the need for persistent service daemons or operator authentication tokens.

The local quickstart builds the current checkout so the guide and binary share the same configuration contract. It requires Git, the Go version in `go.mod`, Node.js/npm, and an installed, authenticated Codex CLI. Authenticate Codex before maintenance; the ACP bridge uses that local setup. Initial bridge startup may need network access to download its pinned npm package.

### SQLite and Native Format Upgrades

The default SQLite binary bundles native cosine SQL, including in CGO-free builds, and needs no separately installed SQLite extension. Embeddings remain disabled until configured and still require their chosen provider. Native format upgrades run numbered Goose SQL on Open: stop old writers, migrate, rebuild, and verify before resuming. PostgreSQL has a separate optional pgvector operator procedure. See [Content and Trust Boundaries](content-and-trust-boundaries.md) for native projection and recovery semantics.

### Prepare a Demo Project

From a cloned Knowl repository:

```bash
KNOWL_DEMO=$(mktemp -d)
go build -o "$KNOWL_DEMO/knowl" ./cmd/knowl
mkdir -p "$KNOWL_DEMO/sources" "$KNOWL_DEMO/.config/knowl"
cp examples/source-to-wiki/sources/authentication-service.md "$KNOWL_DEMO/sources/"
```

### Configuration
Local mode is configured in `.config/knowl/config.yaml`. For the demo, save this as `$KNOWL_DEMO/.config/knowl/config.yaml`:

```yaml
runtime:
  providers:
    codex:
      type: codex_acp
      codex_acp:
        bridge_version: "1.7.3"
knowl:
  provider: codex
  workspace:
    path: knowledge
  storage:
    type: sqlite
    sqlite:
      path: .knowl/knowl.sqlite
  scope: local
  server:
    listen_addr: 127.0.0.1:0
  sources:
    - id: engineering-docs
      type: filesystem
      filesystem:
        root: sources
        include: ["authentication-service.md"]
        flavor: markdown
```

- **Scope & Storage**: Operates under `scope: local` with SQLite storage (defaulting to `.knowl/knowl.sqlite`).
- **Workspace**: Maintains the semantic Markdown wiki under `knowledge/wiki/` and preserves immutable accepted source revisions under `knowledge/raw/`.
- **Maintainer**: Uses an ACP maintainer provider (e.g., `codex_acp` with a pinned `bridge_version: "1.7.3"`) executing over local stdio. The example uses the agent's default model; an available account-specific identifier can be set through `model` under `codex_acp`.
- **Server**: Sets `server.listen_addr: 127.0.0.1:0`; these one-shot commands do not start HTTP/MCP listeners.

### Lifecycle and CLI Workflow
Local execution relies on sequential CLI commands:
- `knowl init`: Initializes project state and directory layout.
- `knowl run --source <source_id>`: Synchronizes source revisions and executes maintainer wiki updates.
- `knowl validate`: Checks required workspace paths and validates the root index and log controls.
- `knowl source status <source_id>`: Inspects sync status; maintenance must be completed rather than just scanned.
- `knowl retrieve '<query>'`: Retrieves bounded evidence containing matching page identifiers, excerpts, and provenance source references.

For the prepared demo:

```bash
cd "$KNOWL_DEMO"
./knowl init
./knowl run --source engineering-docs
./knowl validate
./knowl source status engineering-docs
./knowl retrieve 'authentication session revocation'
```

Source status must show completed maintenance. Retrieval should contain a nonempty `evidence` array with page identities, excerpts, and source references. Generated titles and paths depend on the agent; follow actual evidence rather than assuming an output filename. If maintenance fails, inspect its operation and repair agent installation, authentication, or model selection before retrying. See [Service Operations](service-operations.md) for recovery.

Keep the demo directory for wiki and provenance inspection. Adopting this workflow in an existing project requires deliberately adding configuration and sources while preserving existing knowledge and credentials. Browser access requires enabling the Web UI with a fixed listener and operator token; an embedding API or local TEI server is optional. Codex consumer plugin setup is separate from this maintainer quickstart.

## Service Mode
Service mode runs Knowl as a persistent MCP and HTTP service with dedicated operational storage and multi-client retrieval capabilities.

### Deployment and Configuration
Service mode is typically deployed using Docker Compose (`deploy/sidecar/compose.yaml`):
- **Environment Variables**: Requires `OPENAI_API_KEY`, `OPENAI_MODEL`, and `KNOWL_OPERATOR_TOKEN` (used for bearer authentication).
- **Execution & Storage**: Runs as a non-root user, listening on port 8080 inside Docker and publishing only to host loopback (`127.0.0.1:8080`). It owns `/var/lib/knowl/knowledge` with SQLite database storage under `.knowl/` persisted across restarts in the `knowl-data` named volume. Sources (e.g., `/sources/engineering`) are mounted read-only.
- **Maintainer**: Uses a configured provider (such as the OpenAI maintainer provider) or an ACP agent executable supplied inside the container.

### Client Interfaces
- **HTTP / REST API**:
  - Evidence retrieval: `GET /v1/retrieve` with Bearer token authentication in the `Authorization` header and query parameter `query`.
  - Ingestion: `POST /v1/ingest`.
  - Status: `GET /v1/operations/{operation_id}`.
- **Streamable HTTP MCP**:
  - Connects at `http://127.0.0.1:8080/mcp` using `transport: streamable_http` and Bearer token headers.
  - Exposes `knowl_retrieve`, `knowl_ingest`, and `knowl_operation` tools.
- **Bundled Web UI**:
  - Enabled via `knowl.web.enabled: true` and authenticated using `knowl.operator.token`.
  - Accessible at `http://127.0.0.1:8080/ui/` with four server-rendered screens powered by bundled, pinned local assets without Node or frontend build tooling:
    - **Knowledge**: Renders the canonical wiki root, catalog links, breadcrumbs, an expandable directory file tree ("All pages"), page metadata ("Details"), and immutable accepted text inspection ("Page sources" / "Read saved source").
    - **Search**: Interactive full-wiki search indicating embedding provider status, displaying ranked evidence cards with retrieval badges (`lexical`, `hybrid`, `degraded`, `failed`), and supporting JSON export.
    - **Operations**: Stored task history filtered by status and source ID, detailing execution, retry status, maintainer context fitting, plan counts, and correction reports.
    - **Sources**: Lists configured sources and documents tracking upstream heads, accepted revisions, processing revisions, and synchronization history.

### Health and Operations
- **Health Endpoints**:
  - `/healthz`: Indicates the HTTP process is running.
  - `/readyz`: Verifies operational storage, recovery status, and retrieval readiness.
- **Maintenance Status**: Checked via container CLI execution: `knowl --config-dir /etc source status <source_id>`.
- **Persistence**: Volumes retain `raw/` history, `wiki/` markdown bundles, source statuses, and operational recovery state.

### Browser Connection and Navigation

Web access is disabled by default. Merge the following overlay into an existing valid configuration, set the named environment variable, and restart `knowl start`:

```yaml
knowl:
  web:
    enabled: true
  operator:
    token: ${KNOWL_OPERATOR_TOKEN}
```

Enabled web access requires a nonempty token. At the default listener, open `http://127.0.0.1:8080/ui/`, enter the secret, and select **Connect**. The public shell/assets contain no workspace data; workspace reads require authentication. The token stays in current-document memory, survives connected UI navigation, and clears on reload or **Disconnect**. It also permits agent API ingestion despite the UI's read-only controls. Access beyond loopback uses a trusted HTTPS proxy; secret overrides stay out of Git.

Knowledge renders the actual root `index.md` body. Breadcrumbs follow verified catalogs. Direct links and reload use the root and any verified current catalog; deeper ancestry is not inferred from filesystem paths.

**All pages** groups files by wiki directory path. Expanding folders loads children lazily; **More files** continues a branch. Each `index.md` appears as an ordinary file, and opening an article requires explicit selection. Browser Back restores the loaded/expanded tree within the connected tab while its bounded in-memory view remains available; reload/disconnect clears it.

**Details** starts closed and shows identity, digest/version, and available type, description, tags, status, trust, and stale metadata. **Page sources** starts closed at every screen width. Leaf pages without references say so; catalog indexes have no source panel.

**Read saved source** opens immutable accepted text with digest/media type immediately after the source record and focuses its loading, text, or error state. **Close saved source** clears the reader and returns focus to its read button. Unavailable revisions produce explicit errors without upstream fetching. **Open original** requires allowed credential-free HTTP/HTTPS provenance; filesystem sources may lack such a URL while retaining readable saved text. Browsing does not automatically load remote images or upstream content. Citations support whole pages rather than individual sentences; current canonical pages and immutable accepted history have independent identities.

### Browser Search and Processing Inspection

Search starts with a blank labeled input. Only explicit **Search** submission invokes retrieval, once per submission; typing or opening the screen does not. The disclosure states whether embeddings are enabled, in which case submission sends the query to the configured provider. Page/source browsing does not send embedding queries.

Evidence retains server order, original snippets, and source refs. Available badges distinguish lexical, hybrid, degraded, and failed retrieval. Successful empty results differ from failure; missing diagnostics are unavailable. **View JSON** and **Export JSON** reuse the cards' safe response without another retrieval or token disclosure. Evidence titles open current pages, which may differ from the earlier result snapshot.

After successful search, navigating to an evidence page in the same tab and using Back restores the query/results without another retrieval. Only one bounded results view is kept; oversized views cannot restore. Fresh Search navigation, reload, and disconnect clear query/results. Search returns evidence rather than a generated answer or chat.

Operations lists show full operation IDs; details also offer an expandable identity. **Back to operations** focuses the selected row or, without one, the list heading. Selecting an operation reveals/focuses details, loading, or errors even in stacked layouts. Selected context lists what maintenance read rather than changed pages or a changeset preview. Corrections differ from execution retries; request-budget exclusions differ from report truncation. Missing historical reports remain unavailable rather than zero.

Selected queued/running operations refresh while the tab is visible. Background polling preserves reading position/focus, pauses in hidden tabs, backs off on transient failure, and stops on terminal status, navigation, or disconnect. **Refresh** reloads the first list page while preserving status/source filters. The screen neither starts work nor retries failures.

Sources opens saved documents first, distinguishing upstream heads, accepted revisions, and processing revisions. **View operation** follows an associated stored operation; **Sync and processing** history appears below the document table. Source selection and **Next documents** reveal/focus requested details, including loading/errors beneath tall lists.

Sync accepts evidence and reserves work; processing can remain queued or failed. Later failed syncs preserve last successful sync and accepted revisions. Only reconciled tombstones establish deletion, not omission from bounded results. General lists start at 50 rows and Operations at 10; **Next** continues bounded lists, which may require refresh after workspace changes or service restart.

### Browser Source-to-Wiki Walkthrough

A fresh temporary demonstration uses the checked-in authentication source, the Go version in `go.mod`, an OpenAI API key, and a model available to that key. Maintenance invokes the real provider; generated names and wording can vary. Existing project configuration and knowledge remain in their original directories.

From the repository root:

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

Save this complete configuration as `$KNOWL_UI_DEMO/.config/knowl/config.yaml`:

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
  storage:
    type: sqlite
    sqlite:
      path: .knowl/knowl.sqlite
  scope: local
  server:
    listen_addr: 127.0.0.1:8086
  web:
    enabled: true
  operator:
    token: ${KNOWL_OPERATOR_TOKEN}
  embeddings:
    enabled: false
  sources:
    - id: engineering-docs
      type: filesystem
      filesystem:
        root: sources
        include: ["authentication-service.md"]
        flavor: markdown
```

Initialize, synchronize/drain maintenance, validate, inspect processing, and start the service:

```bash
cd "$KNOWL_UI_DEMO"
./knowl init
./knowl run --source engineering-docs
./knowl validate
./knowl source status engineering-docs
./knowl start
```

Keep it running and open `http://127.0.0.1:8086/ui/`:

1. Connect with the chosen token.
2. Select `engineering-docs` in Sources and verify accepted revision and completed processing for `authentication-service.md`. Inspect the associated operation if processing failed.
3. In Knowledge, use All pages to find an actual generated authentication page. Open Page sources and Read saved source to inspect immutable accepted text.
4. Search for `session revocation JWT`, inspect evidence/View JSON, open a current page, and use Back to restore results. Embeddings are disabled, so retrieval remains local.
5. Follow the document's View operation link and compare stored processing facts with its accepted revision. Absent legacy reports remain unavailable.

Ctrl-C stops the service. The temporary workspace can be retained to inspect `raw/` and `wiki/`; publication is not automatic. Operators own Git review, commits, and remote publication.

A deterministic repository fixture verifies source acceptance, maintenance, and retrieval with a test maintainer and fresh workspace, without a live model, browser launch, or changes to the checked-in showcase:

```bash
go test -count=1 ./examples/source-to-wiki \
  -run '^TestSourceToWikiShowcaseEndToEnd$'
```

## Maintainer Compatibility Across Deployments

Codex ACP configuration can select `model`, `reasoning_effort`, and `mode` capabilities supported by the agent. Omitted selections use agent defaults; a configured model must also be available to the authenticated account.

For custom `generic_acp` executables, `cmd` is an argv prefix and `extra_args` appends arguments without interpreting shell syntax. An example `--stdio` argument is illustrative and is appropriate only when the selected executable accepts it. The agent must implement the required ACP session behavior and return plans satisfying Knowl's structured output contract. Protocol compatibility does not establish that every third-party agent has been tested; validate a setup with one source and inspect its maintenance operation before processing a large collection.

The documented service example uses the hosted OpenAI provider, so its base container requires no separately installed ACP executable. The repository's own-documentation example uses Antigravity through ACP.

## Repository Documentation Workflow

The repository's `.config/knowl/config.yaml` configures a project-local documentation corpus and maintenance workflow:

| Responsibility | Repository configuration |
| --- | --- |
| Source | `knowl-docs`, filesystem root `docs`, include `**/*.md` |
| Maintainer | `antigravity_acp`, model `gemini-3.8-flash-medium` |
| Published wiki | `knowledge/wiki/` |
| Accepted revisions | `knowledge/raw/` |
| Operational state | SQLite under `knowledge/.knowl/` |
| Search | Hybrid with local E5-base at `127.0.0.1:8091`, explicit lexical fallback |

These are repository example settings. Maintainer selection and embedding configuration remain independent. The agent synthesizes connected semantic pages from documentation rather than copying its directory layout.

Generation from the repository root requires the Go version in `go.mod`, Task, Node.js/npm, and an authenticated Antigravity runtime with access to the configured model. The Taskfile resolves the pinned ACP runner/bridge; a cold run needs network access to download it. The configured agent receives selected documentation as source material. A local embedding server enables hybrid retrieval; the checked-in fallback policy permits lexical retrieval when that server is unavailable.

```bash
task wiki:generate
```

This resolves `antigravity-acp`, runs `knowl run --source knowl-docs`, and validates the workspace. It synchronizes revisions, drains ready maintenance, and reconciles semantic navigation while preserving accepted raw history. Git publication remains a separate repository workflow.

Processing, retrieval, structural validation, and generated changes can be inspected with:

```bash
go run ./cmd/knowl source status knowl-docs
go run ./cmd/knowl retrieve 'How do I configure an ACP maintainer?'
task wiki:validate
git diff --stat -- knowledge
git diff -- knowledge/wiki
```

Inspect maintenance counts and operation samples alongside scan results. A successful scan or structural validation alone does not establish that all agent updates completed. Failed or deferred work needs recovery, and retrieval mode distinguishes hybrid results from degraded lexical fallback.

Review generated facts against cited accepted revisions. Factual pages carry `knowl.source_refs`; the Web UI exposes them through **Page sources** and **Read saved source**. Saved revisions and the wiki may lag the latest documentation until maintenance completes. New Markdown files under `docs/` join the corpus, so credentials and local overrides must stay outside it.

Commit and publish reviewed wiki/raw changes through the repository's normal Git workflow. Preserve immutable raw history when correcting pages or rolling back changes. See [Service Operations](service-operations.md) for maintenance recovery and [Content and Trust Boundaries](content-and-trust-boundaries.md) for provenance, storage, and export.
