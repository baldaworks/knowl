---
type: topic
title: Architecture
knowl:
  id: concepts/architecture
  source_refs:
    - wiki-filesystem:knowl-docs/README.md@5dc9ddce38b85082810b9f03e88921ba503abaebd974be1dacb48fde1069a10d
    - wiki-filesystem:knowl-docs/agents.md@e0b710a8a9c32653f3580c09abb08dbbe32b2322de428c63a60bae39370b377b
    - wiki-filesystem:knowl-docs/design.md@a3099f6ef8a7094ced149ddd0e73f9b95c5a9a44c8d8007c6ec3f4b764aa4f41
    - wiki-filesystem:knowl-docs/examples/own-docs-wiki.md@4b076ff94b528e7e337477c9190b8aa2b59649bb07e78bd65db880b1b28c8fa2
    - wiki-filesystem:knowl-docs/local-codex.md@50fcfaf27cd8c671650b42f050c7b90bfcef1355619133c9d9da92ffa3ccc0f9
    - wiki-filesystem:knowl-docs/operations.md@c4dc7e57ca23dc8864f014dc283faf6fee92c4de46685fea364e4bade6fd6e14
    - wiki-filesystem:knowl-docs/publishing.md@a37755a2d562c9f596a41e474422161f29278ba9d9e527e3375224a8ce4265b4
    - wiki-filesystem:knowl-docs/releases/v0.1.0.md@183d6c7f742a0555fa5f792ea5d84435ede6851d29fb5007301c8e8144bfb561
    - wiki-filesystem:knowl-docs/releases/v0.2.0.md@4d7a07f5afbdfc49ae07e300c851d7f8ab6a03bbb25b93c42203f8102c18bfe0
    - wiki-filesystem:knowl-docs/releases/v0.3.0.md@68b799fdc477f93914692c3f66603905eee292e9bb64a1362063b090f319bee1
    - wiki-filesystem:knowl-docs/releases/v0.3.1.md@b1edbf1e4b747269157429171f860cbf7f1dce300304dedc7e75b6f528c0dd30
    - wiki-filesystem:knowl-docs/releases/v0.4.0.md@d4c8e0eb36bbb174da0c8d166fadc1719e46376f3fc059bfd6d9dc651148f45c
    - wiki-filesystem:knowl-docs/releases/v0.5.0.md@a5c47b8777193be18a796d6a230a8af26040ea368d6b9ef1447610bf023f08a7
    - wiki-filesystem:knowl-docs/releases/v0.6.0.md@8d47fd9686a95f88837e18b9632061faa2fa3d6372ae3645b70ff5fda3985487
    - wiki-filesystem:knowl-docs/search.md@8edcc0f5bb92cbd71623973266306b8fc0e2398e5a10e39ca0637680e2231952
    - wiki-filesystem:knowl-docs/testing.md@b0e4cf0419351caee01df101a02a5de985f9e19b6370980058409bd2d794f8b8
    - wiki-filesystem:knowl-docs/workspace.md@b5ea99ea7028c5e2b08444733272f1a7f9f7b306b66c558f28b7fcaa612844f2
---
# Architecture

Knowl is a self-hosted LLM wiki that turns durable project knowledge into an inspectable Markdown wiki with OKF-compatible storage and returns bounded, provenance-backed evidence across two [Execution Modes](execution-modes.md).

## Product Boundaries and Host Responsibilities
- **Host Responsibilities**: Decides which events and source revisions are durable, manages session context, orchestrates tools, and generates final user answers. Knowl is neither temporal session memory nor a multi-tenant platform.
- **Knowl Responsibilities**: Owns durable acceptance, canonical raw and Markdown artifacts, operation recovery, and bounded evidence with source references.

## Public Business Contract
Knowl defines exactly three public business operations exposed identically over MCP and HTTP/OpenAPI:
- **Retrieve Evidence**: `knowl_retrieve` (MCP) / `GET /v1/retrieve` (HTTP).
- **Ingest Source**: `knowl_ingest` (MCP) / `POST /v1/ingest` (HTTP), taking either inline `content` or a `uri` reference.
- **Operation Status**: `knowl_operation` (MCP) / `GET /v1/operations/{operation_id}` (HTTP).

Operational endpoints (`GET /healthz`, `GET /readyz`) provide liveness and readiness checks. Direct page CRUD and raw workspace writes are not exposed.

## Content and Storage Trust Boundaries
- **Canonical Filesystem**:
  - `raw/`: Stores immutable accepted source revisions and manifests (e.g., `knowledge/raw/<scope/source identity>/<version>/source` and `manifest.yaml`). Logical source identity is the tuple `(scope, adapter, source ID, version)`.
  - `wiki/`: Stores human-readable, directly portable semantic OKF v0.2 bundle files (`index.md`, `log.md`, `catalogs/<domain>/index.md`, `entities/`, `concepts/`, `syntheses/`). Pages cite accepted sources via `knowl.source_refs` and link using standard relative Markdown links with explicit `.md` destinations. Generated navigational double-bracket links and destinations that escape the wiki are rejected. Legacy wikilinks remain readable, and literal code examples retain their syntax.
  - `schema.md`: Operator-controlled Markdown policy (`schema_version: 1`) providing untrusted guidance to the maintainer. Maintainer update plans cannot modify `schema.md`.
  - Control files (`wiki/index.md`, reserved `index.md`, `log.md`): Excluded from retrieval evidence. `wiki/index.md` is the root catalog declaring `okf_version: "0.2"`. `wiki/log.md` records committed operations as ISO-date-grouped Markdown entries with inert JSON operation comments.
- **Operational Storage**: SQLite (default for local and service modes under `.knowl/`) and PostgreSQL store rebuildable state. Lexical indexing projects four semantic fields in descending priority: title, normalized OKF tags, OKF description, and body.
- **Implementation State**: `.knowl/staging/` holds staged updates for preview and atomic commits; `.knowl/recovery/` maintains journal preimages across crashes.

## Protocol Roles: ACP vs MCP
- **ACP (Agent Client Protocol)**: Connects Knowl to its wiki maintainer over stdio. Maintainer agents read sources, propose wiki update plans, and submit structured output that Knowl validates before publishing.
- **MCP (Model Context Protocol)**: Connects consumer agents to Knowl's retrieval, ingest, and operational tools.
- Agents such as Codex can act as a maintainer via ACP, a consumer via MCP, or both.

## Maintainer Agents and Provider Registry
The active wiki maintainer is selected using the `knowl.provider` setting from the `runtime.providers` registry:
- **Codex (`codex_acp`)**: Launched through `npx` with `bridge_version: "1.7.3"`. Accepts optional `model`, `reasoning_effort`, and `mode` settings.
- **Custom Agents (`generic_acp`)**: Configured with `cmd` (argv prefix) and optional `extra_args`. Communicates via ACP on stdin/stdout and must implement ACP session behavior satisfying Knowl's structured output contract.
- **Hosted Providers & Adapters**: Supports hosted model APIs (e.g., OpenAI provider in service mode) and named ACP adapters like `antigravity_acp` (e.g., paired with model `gemini-3.8-flash-medium` in reference repository configurations).

## Codex Client Integration and Skills
Codex integrates as a consumer client via the `@baldaworks/knowl` npm package (e.g. `0.6.0`):
- **Setup**: `npx --yes @baldaworks/knowl@0.6.0 setup codex` creates missing project state (`.config/knowl`, `schema.md`, `raw/`, `wiki/`, `.knowl/`), validates workspace structure, and registers the release-matched marketplace plugin (replacing existing only with `--replace`).
- **Workflow Skills**:
  - `$knowl:setup`: Repeats or repairs the pinned setup workflow.
  - `$knowl:run`: Validates workspace state, executes one bounded `knowl run` (using local binary if `knowl version --json` reports `0.6.0`, otherwise the pinned `npx` launcher), re-validates, and reports structured changes. Maintenance requires an installed and authenticated ACP runtime (e.g., `opencode acp`).
- **Consumer MCP**: Registers a project-scoped MCP server running `npx --yes @baldaworks/knowl@0.6.0 mcp stdio` owned directly by Codex without background daemons, ports, or operator tokens.
- **Scope**: Verified for local Codex instances on local filesystems with shared store and workspace coordination across threads.

## Search and Hybrid Retrieval
Knowl uses lexical search by default, matching indexed page titles, tags, descriptions, and body content. When embeddings are configured, hybrid search combines lexical and semantic candidates from the same published wiki.
- **Search Modes**: Retrieval modes include `lexical`, `hybrid`, `degraded`, and `failed`. Public transports expose only effective mode and reason; internal reports track detailed coverage and candidate counts.
- **Embedding Configuration**: Configured under `knowl.embeddings` with settings for `enabled` (default false), `endpoint` (OpenAI-compatible float embedding URL, max 2,048 bytes), `model` and `revision` (declaring immutable weights, max 256 bytes), `dimensions` (1–4,096), `query_prefix`, `passage_prefix`, and `failure_policy`. Pinned reference setup uses local TEI 1.9.0 with E5-base (`intfloat/multilingual-e5-base`, revision `d128750597153bb5987e10b1c3493a34e5a4502a`, 768 dimensions). Bearer credentials can be loaded via `api_key_env`.
- **Failure Policies**:
  - `failure_policy: lexical`: Classified embedding failures fall back to lexical evidence with effective mode `degraded` and a safe reason. `/readyz` can succeed while semantic retrieval is degraded.
  - `failure_policy: strict`: Dense retrieval errors fail typed requests directly.
- **Bounds and Limits**:
  - Request: At most 16 inputs, 2,048 UTF-8 bytes each (including prefix), 64 KiB serialized body.
  - Response: At most 1 MiB, finite nonzero normalized float vectors.
  - Inference: 10-second request deadline, 4 concurrent client requests, no hidden retries.
  - Semantic Chunking: Heading sections split at block boundaries (up to 384 runes per input). Queries and source signals use up to four 384-rune windows with 64-rune overlap.
  - Projection Limits: At most 8,192 chunks, 1 MiB page coverage metadata, and 64 MiB total per scope; 15-minute rebuild timeout.
- **Projection Lifecycle**: Rebuilds re-embed pages in batches of up to 16 without holding database locks. Prior vectors are reused when model space, prior state, and prepared inputs match. Queries never trigger rebuilds or model downloads.

## Execution, Lifecycle, and CLI Operations
- **Workers**: `knowl.workers` configures 1 or 2 fixed execution owners (defaults to 1; only 1 and 2 are supported). Each worker owns an independent maintainer runtime and session, sharing capacity across the scheduler, Drain/RunOnce, explicit hierarchy, and model-lint calls.
- **Publication Gate**: A single host publication gate serializes canonical commits, snapshot projections, and recovery outcomes. Precondition mismatches fail permanently with class `canonical_conflict` and reason `precondition_failed`.
- **Output Correction**: `knowl.output.max_corrections` defaults to 1 (accepts 0 or 1). Shared loop validates plans; all turns share 1 MiB output ceiling and a 5-minute deadline. Safe feedback sends a single typed code (`structured_output_invalid`, `source_plan_invalid`, `hierarchy_plan_invalid`) with a 128-byte prompt reserve.
- **Source Maintenance Limits**: Complete prompt cap defaults to 4,194,304 bytes (4 MiB) with 128 bytes reserved. Catalog limits include 1,024 catalogs, 16,384 edges, depth 16, 2,048 max path bytes, and 262,144 max catalog bytes.
- **Execution Run & Lifecycle**: Running `knowl run --source <source_id>` synchronizes source revisions, drains ready maintenance work, and reconciles semantic navigation while preserving existing accepted raw history (it does not push Git changes).
- **Workspace Commands**:
  - `knowl init`: Creates required directories, starter `schema.md`, root `wiki/index.md`, and `wiki/log.md`.
  - `knowl validate`: Checks required workspace paths and validates the root index and log controls.
  - `knowl export okf --output <dir>`: Exports the standalone OKF v0.2 bundle without raw evidence, `.knowl/`, or database files.
  - `knowl export llms-txt [--output <file>] [--base-url <url>] [--title <title>] [--summary <summary>]`: Generates bundle-relative `llms.txt` navigation.
  - `knowl migrate okf-v0.2`: Executes explicit legacy workspace migration with recovery journaling.
- **Semantic Hierarchy Reconciliation**: Explicitly reorganizes ordinary pages using `knowl hierarchy reconcile` (planning contract `hierarchy-v3`). Reconciles subject-first catalogs under strict bounds (1,024 pages, 1,024 catalogs, 16,384 edges, depth 16, 4 MiB total input, 4,096 excerpt chars/page, 1 MiB plan output, 256 KiB per catalog).
- **Recovery Journaling**: Operations write staged manifests and preimage journals. Startup rolls back `prepared` journals and cleans completed `committed` journals.
- **Inspection & Recovery**: The operator CLI (`cmd/knowl`) provides commands such as `source status <source_id>` and `source retry <source_id> --failure-class <class>` (supporting `--dry-run` previews). Stored work under incompatible output policies fails with `maintenance_policy_mismatch`.
- **Retrieval Fallback**: When hybrid search is configured (e.g., local E5-base embedding server), an unavailable server permits fallback to lexical retrieval when configured with an explicit lexical fallback policy.

## Testing and Verification Baseline
Knowl uses automated regression baselines and strict contract gates across all layers:
- **Context and Ingest Baseline (`context-baseline-v1`)**: Measures retrieval recall, source title/heading signals, catalog scaling (up to 32 catalogs), and provider input budget fitting (enforcing the 4 MiB prompt envelope). Offline runs need no containers or model services:
  ```sh
  go test -count=1 -json ./pkg/knowl/... -run 'TestContextBaseline|TestIngestRejectsStaleReviewedPlan' > /tmp/knowl-context-baseline.jsonl
  ```
- **Verification Preflight**: Complete pre-push validation requires running unit tests, strict linting, and architecture boundary checks:
  ```sh
  go test ./...
  go tool golangci-lint run ./...
  go run github.com/fe3dback/go-arch-lint@v1.15.0 check --project-path .
  ```
- **PostgreSQL Integration**: Containerized storage contract testing runs under the `integration` tag:
  ```sh
  go test -tags integration -count=1 ./pkg/knowl/store/postgres -run TestStoreContractWithTestcontainers
  ```
- **Quality Gates**: Pinned evaluations verify real CPU TEI embedding quality (E5-base with 768 dimensions), output correction sequence enforcement, and 1-to-2 worker concurrency without data races or orphaned leases.

## Usage Surfaces and Package Layering
Dependencies follow strict inward layering:
- `pkg/knowl/types`: Shared IDs and data shapes with no Knowl dependencies.
- `pkg/knowl/wiki`: Frontmatter and wiki semantics.
- `pkg/knowl/app`: Business policy and consuming ports.
- Adapters: `content/fs`, `store/*`, `provider`, `pkg/knowl/mcp`, `internal/httpapi`, `internal/source`.
- `pkg/knowl`: Composition root and host API composing adapters.
- Entrypoints: `pkg/knowlfx` (Fx lifecycle) and `cmd/knowl` (operator CLI).

## Documentation and Release Policy
Current documentation describes the checked-in source. Recipes for published releases name versions explicitly, and historical release recipes do not imply that older binaries include newer features.

### Release History and Container Images
- **Published Images**: Container deployments pull `ghcr.io/baldaworks/knowl:<version>` (e.g., `ghcr.io/baldaworks/knowl:v0.1.0`), pinning the immutable manifest digest `ghcr.io/baldaworks/knowl@sha256:<published-digest>` in production, and mounting persistent storage to `/var/lib/knowl`.
- **Historical Releases**:
  - **v0.1.0**: Established the initial crash-safe knowledge loop, proving restart survival for immutable raw sources and descriptors, at-least-once execution to terminal operation status, and idempotent commits. Early limitations (no UI, embeddings, or Git sync) represent historical release state that is superseded in current versions.
  - **v0.2.0**: Added multi-source filesystem synchronization with `(source_id, document_id, revision)` identity, canonical OKF v0.2 bundle representation, inert Attested Computation metadata, source-filtered retrieval, and CLI bootstrap/migration commands (`knowl bootstrap`, `knowl migrate okf-v0.2`).
  - **v0.3.0**: Introduced semantic source maintenance, requiring a configured `knowl.provider` before host readiness, eliminating active `wiki/sources/<source_id>/**` mirrors in favor of pure `raw/` revision intake, supporting multi-source evidence synthesis into shared semantic pages with lineage preservation, and cleaning up legacy source mirrors via staged recovery.
  - **v0.3.1**: Added one-way conflict-safe legacy provenance backfill during reconciliation, enriching pre-provenance raw manifests with validated `source_document` identities and rebuilding projections to restore source-filtered retrieval without re-fetching accepted raw bytes.
  - **v0.4.0**: Added remote Git sources with HTTPS/SSH authentication, one-shot `knowl run` execution, explicit semantic hierarchy planning (`knowl hierarchy reconcile`), export commands (`knowl export okf`, `knowl export llms-txt`), semantic OKF tag indexing with relaxed fallback, and expanded default document limits to 262,144 characters.
  - **v0.5.0**: Introduced policy-aware maintenance tracking via deterministic policy generation (reconsidering unchanged revisions when rules like character ceilings change), preserved open-world links from accepted Markdown with bounded diagnostics, isolated invalid provenance rejections to affected documents, and hardened Git cache lineage and repository identity checks.
  - **v0.6.0**: Added repository-supplied local Codex plugin and staged `@baldaworks/knowl@0.6.0` npm package with five native platform binaries, enabling project bootstrapping via `npx`, scoped MCP stdio registration, and exact version enforcement in the run skill.

### Release Publication and npm Trusted Publishing
- **Workflow and Triggers**: The release workflow is `.github/workflows/release.yml`, executing for annotated `vX.Y.Z` tags whose commits belong to `main`. The Go version is sourced from `go.mod`, while `.omnidist/omnidist.yaml` defines the native npm target matrix and package identity.
- **npm Trusted Publishing**: Omnidist uses `publish-auth: trusted` with repository URL `git+https://github.com/baldaworks/knowl.git`. The publishing job operates with `id-token: write` and installs npm 12.2.0 without requiring an npm token secret.
- **Trusted Publisher Configuration**: An npm package owner must configure GitHub Actions as a trusted publisher for `@baldaworks/knowl` and the five native packages (`@baldaworks/knowl-darwin-x64`, `@baldaworks/knowl-darwin-arm64`, `@baldaworks/knowl-linux-x64`, `@baldaworks/knowl-linux-arm64`, `@baldaworks/knowl-win32-x64`), using organization `baldaworks`, repository `knowl`, workflow filename `release.yml`, and direct `npm publish` authorization.
- **Trust Registration Commands**: Commands derived from Omnidist configuration can be inspected and registered:
  ```bash
  npx -y @omnidist/omnidist@latest --profile default npm trust \
    --repo baldaworks/knowl --workflow-file release.yml
  ```
  Adding `--apply` registers bindings, requiring existing packages, owner write access, and account 2FA (unpublished packages require an initial authenticated publication before trust registration).
- **Release Verification & Cutting**: Releases require version alignment across plugins, skill contracts, metadata, and installation guides. Verification runs `go run ./internal/cmd/releasecheck --version X.Y.Z`, repository tests, and linters before merging into `main` and tagging. The workflow stages native npm packages and the launcher, publishing them alongside the multi-platform GHCR image and GitHub Release with immutable digest only after all verification passes. Public tags are never moved; fixes require a new patch version.
