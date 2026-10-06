---
type: topic
title: Public Contract
knowl:
  id: concepts/public-contract
  source_refs:
    - wiki-filesystem:knowl-docs/design.md@3a68ce3bd879e93c51035a4f8f0cf5c198e5019718caf580e388c947b63f1749
---
# Public Contract

Knowl defines a minimal public business contract consisting of exactly three operations:
1. Retrieve bounded evidence.
2. Ingest one source.
3. Read durable operation status.

## Transports and Protocols

- **MCP**: Primary agent-facing interface providing `knowl_retrieve`, `knowl_ingest`, and `knowl_operation`.
- **HTTP/OpenAPI**: Deterministic host control transport providing `GET /v1/retrieve`, `POST /v1/ingest`, and `GET /v1/operations/{operation_id}`. Operational endpoints `GET /healthz` and `GET /readyz` provide health checks rather than business operations.
- **Go Embedding**: In-process execution through `pkg/knowl` or `pkg/knowlfx` (Fx lifecycle integration) using the same underlying host runtime.
- **CLI**: `cmd/knowl` is a local convenience wrapper around the same core operations, not an independent agent interface.

## Boundary Constraints

Transports do not expose direct page CRUD, raw workspace file writes, search sub-steps, or public review/apply workflows.

Public ingest accepts source text or a URI reference; submitting a URI does not download its contents. Configured filesystem and remote Git sources use the separate operator synchronization lifecycle to fetch documents and reserve durable maintenance work. Git synchronization preserves the same three-operation public contract.

See [[concepts/architecture]] for system design and [[concepts/content-and-trust-boundaries]] for workspace structure.
