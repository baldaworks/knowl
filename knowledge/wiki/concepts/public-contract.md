---
type: topic
title: Public Contract
knowl:
  id: concepts/public-contract
  source_refs:
    - wiki-filesystem:knowl-docs/design.md@c9a981be23b39a137da77ad85726d8382d33306b44e801022bcdd4de4b525132
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

## Ingest Input Semantics

Public ingest accepts exactly one nonempty `content` or `uri`. Both transports trim surrounding whitespace before normalization.

A `uri` input stores the URI string as a reference, defaults its media type to `text/uri-list`, and does not download the referenced document. To ingest document contents, the caller supplies obtained text in `content`. Optional `origin` is a source identity hint; it neither triggers a fetch nor automatically populates structured source-document URI metadata.

Configured filesystem and remote Git sources use the separate operator synchronization lifecycle to fetch documents and reserve durable maintenance work. Git synchronization preserves the same three-operation public contract.

## Operator Web UI and Read API

- **Operator Web UI**: An optional server-rendered web UI adding four screens: Knowledge, Search, Operations, and Sources. Disabled by default, sharing the existing HTTP listener. Enabled access requires the operator token, which also authorizes existing agent write endpoints.
- **Operator Read API**: A separate API exposing eight GET routes bound to the configured workspace and scope (`api/openapi/operator.yaml`). It adds no agent business operations.
- **Read-Only Operation**: The UI reads current canonical pages and immutable accepted raw revisions with page-level reference support without claim-level attribution. Search executes the existing retrieval path once per query; optional embeddings may send the query to the configured provider. Browsing does not invoke a maintainer or fetch upstream sources; maintenance and source schedules continue independently. Browser mutations, chat, edit controls, and changeset review are excluded.

## Boundary Constraints

Transports do not expose direct page CRUD, raw workspace file writes, search sub-steps, or public review/apply workflows. Public callers cannot override the trusted scope assigned by the host. Knowl returns bounded evidence and operation status; the host owns session context, orchestration, and final-answer generation.

See [Architecture](architecture.md) for system design and [Content and Trust Boundaries](content-and-trust-boundaries.md) for workspace structure, retrieval integrity, and lifecycle authority.
