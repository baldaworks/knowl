---
type: topic
title: Public Contract
knowl:
  id: concepts/public-contract
  source_refs:
    - wiki-filesystem:knowl-docs/design.md@0f73c5118fde6973472cf8aed129de3ffedabf43caac1f46fdfebeccaa6a7167
---
# Public Contract

Knowl defines a minimal public business contract consisting of exactly three operations:
1. Retrieve bounded evidence.
2. Ingest one source.
3. Read durable operation status.

## Transports and Protocols

- **MCP**: Primary agent-facing interface providing `knowl_retrieve`, `knowl_ingest`, and `knowl_operation`.
- **HTTP/OpenAPI**: Deterministic host control transport providing `GET /v1/retrieve`, `POST /v1/ingest`, and `GET /v1/operations/{operation_id}`. Operational endpoints `GET /healthz` and `GET /readyz` provide health checks rather than business operations. Authoritative HTTP schema is `api/openapi/knowl.yaml`.
- **Go Embedding**: In-process execution through `pkg/knowl` or `pkg/knowlfx` (Fx lifecycle integration) using the same underlying host runtime. Embedded Go templates and pinned local assets eliminate any Node runtime or frontend build step requirement.
- **CLI**: `cmd/knowl` is a local convenience wrapper around the same core operations, not an independent agent interface.

## Operator Web UI and Read API

- **Operator Web UI**: An optional server-rendered web UI adding four screens: Knowledge, Search, Operations, and Sources. Disabled by default, sharing the existing HTTP listener. Enabled access requires the operator token, which also authorizes existing agent write endpoints.
- **Operator Read API**: A separate API exposing seven GET routes bound to the configured workspace and scope (`api/openapi/operator.yaml`). It adds no agent business operations.
- **Read-Only Operation**: The UI reads current canonical pages and immutable accepted raw revisions with page-level reference support without claim-level attribution. Search executes the existing retrieval path once per query (optional embeddings may query the provider). Browsing does not invoke a maintainer or fetch upstream sources; maintenance and source schedules continue independently. Browser mutations, chat, edit controls, and changeset review are excluded.

## Boundary Constraints

Transports do not expose direct page CRUD, raw workspace file writes, search sub-steps, or public review/apply workflows.

Public ingest accepts source text or a URI reference; submitting a URI does not download its contents. Configured filesystem and remote Git sources use the separate operator synchronization lifecycle to fetch documents and reserve durable maintenance work. Git synchronization preserves the same three-operation public contract.

See [[concepts/architecture]] for system design and [[concepts/content-and-trust-boundaries]] for workspace structure.
