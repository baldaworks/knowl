# Product requirements

This document fixes the accepted product requirements for Knowl.

If implementation details drift from this document, the implementation is the
thing to change.

## Product purpose

Knowl is a service-first LLM-wiki knowledge service.

Its job is to:

- accept durable source material;
- maintain a readable Markdown knowledge workspace;
- preserve provenance from accepted sources to wiki pages and retrieval output;
- return bounded grounded evidence to a host agent or application.

Knowl is not:

- session memory;
- user-fact or temporal memory;
- agent orchestration;
- the primary final-answer generator;
- a generic memory platform.

## Primary use cases

Knowl must support these baseline use cases:

1. bootstrap an existing wiki or Obsidian-style Markdown corpus into a
   Knowl-owned workspace;
2. ingest one new text or URI source through a single canonical pipeline;
3. retrieve bounded evidence and provenance for a host query;
4. expose durable operation status for ingest progress or failure;
5. run as a sidecar service or in-process Go runtime over the same business
   semantics.

## Functional requirements

### Canonical workspace

Knowl must keep canonical knowledge in the filesystem workspace:

- `raw/` for immutable accepted source versions;
- `wiki/` for human-readable canonical Markdown;
- `schema.md` for operator-owned policy;
- `wiki/index.md` and `wiki/log.md` for maintained control files.

SQL state is operational and rebuildable. It is not canonical content.

### One ingest pipeline

Knowl must have one accepted public ingest contract.

That contract may accept:

- inline text content;
- a URI;
- stable origin and idempotency hints.

That contract must not accept:

- direct page IDs;
- direct Markdown paths;
- direct raw/workspace writes;
- precomputed changesets that bypass validation.

### Retrieval contract

Knowl must provide bounded retrieval over canonical knowledge and accepted
provenance.

Retrieve results must return:

- evidence derived from maintained wiki content;
- provenance/citation data;
- no direct write authority.

### Durable operation contract

Knowl must expose one public durable operation read model for ingest status.

The public state model is:

```text
queued -> running -> completed | failed
```

### Transport equivalence

Knowl must expose the same business contract through:

- MCP as the primary agent-facing interface;
- deterministic HTTP/OpenAPI as the operator/host control interface.

The accepted business operations are:

- retrieve;
- ingest;
- operation status.

Health and readiness are operational endpoints, not additional business
operations.

### Deployment modes

Knowl must support:

- sidecar/service deployment as the baseline runtime shape;
- Go embedding through Fx over the same host runtime.

The Fx path must not create a second business API.

### Configuration

Knowl configuration must stay aligned with Balda's typed runtime/provider
configuration shape.

Accepted top-level configuration split:

- `runtime:` for shared provider registry;
- `knowl:` for Knowl application settings.

Accepted storage shape:

- `knowl.storage.type`
- `knowl.storage.sqlite`
- `knowl.storage.postgres`

Storage-specific configuration must live in typed optional sections rather than
one flattened mixed block.

### Trusted scope ownership

Trusted scope belongs to the host/service configuration, not to untrusted
request payloads.

Public callers must not override trusted scope through HTTP or MCP arguments.

## Non-functional requirements

- provenance must remain durable and inspectable;
- local defaults must stay bounded and deterministic;
- host startup must validate workspace, initialize storage, run recovery, and
  prepare retrieval readiness before becoming ready;
- write application to canonical files must preserve one-writer ordering;
- public transports must stay KISS and avoid exposing internal workflow stages
  as separate public APIs.

## Official usage surfaces

These are the accepted external usage surfaces:

- `pkg/knowl` for plain-Go host composition;
- `pkg/knowlfx` for Fx embedding;
- MCP for agent-facing use;
- HTTP/OpenAPI for deterministic control/integration;
- `cmd/knowl` for operator workflow and local convenience.

Everything else is internal implementation detail.

## Explicit non-goals

Knowl does not promise:

- direct public CRUD over wiki pages;
- direct public review/apply choreography as the primary contract;
- vector DB as canonical storage;
- automatic crawling/research as a core product requirement;
- multi-tenant control-plane features;
- final-answer generation.
