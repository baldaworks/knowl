# Knowl architecture

Knowl has one canonical content owner: the workspace. `raw/` contains immutable
accepted source versions; `wiki/` contains the human-readable Markdown
knowledge artifact; `schema.md` is operator-owned policy; `wiki/index.md` and
`wiki/log.md` are maintained workspace files. SQL is never canonical content.

This document derives architecture from [product requirements](requirements.md).
It is not a license to preserve accidental package structure.

The architecture is organized around two questions:

1. what external usage surfaces Knowl supports
2. what internal layers are allowed to know about each other

The goal is not to maximize package count. The goal is to keep each usage
surface narrow and to keep inward dependencies obvious.

## Architectural consequences of the requirements

The requirements imply these constraints:

- one public business contract with three operations: retrieve, ingest, and
  operation status;
- one canonical content owner: the filesystem workspace;
- SQL/search state remains operational and rebuildable;
- MCP and HTTP are transport surfaces over the same app services;
- sidecar/service is the baseline runtime shape, with Fx as an in-process
  alternative over the same host runtime;
- package and file splits exist to preserve readability and ownership, not to
  manufacture more layers than the product requires.

## Usage surfaces

These are the supported ways to use Knowl:

- `pkg/knowl`: plain-Go host API and composition root
- `pkg/knowlfx`: Fx wrapper over `pkg/knowl`
- HTTP/OpenAPI: `internal/httpapi/*`
- `pkg/knowl/mcp`: bounded three-tool MCP adapter
- `internal/mcphttp`: standard Streamable HTTP transport over that adapter
- `cmd/knowl`: operator CLI

Everything else is implementation detail for one of those surfaces, not another
public product surface.

## Layers

The intended dependency flow is:

```text
entrypoints/surfaces -> composition -> adapters -> app policy -> shared types
```

With the current package map:

```text
pkg/knowl/types            shared IDs and data-only shapes
pkg/knowl/wiki             shared wiki/frontmatter semantics
pkg/knowl/app              application policy and consuming ports
pkg/knowl/content/fs       canonical workspace adapter
pkg/knowl/store/*          operational state and projection adapters
pkg/knowl/provider         maintainer provider adapter
pkg/knowl/mcp              MCP adapter surface
internal/mcphttp           MCP Streamable HTTP transport
internal/httpapi/*         HTTP adapter surface
internal/bootstrap         workspace bootstrap/import support
pkg/knowl                  composition root and host API
pkg/knowlfx                Fx entrypoint
cmd/knowl                  CLI entrypoint
```

## Layer rules

- `pkg/knowl/types` is the bottom layer. It should not depend on other Knowl
  packages.
- `pkg/knowl/wiki` may depend on `pkg/knowl/types`, because it only adds wiki
  formatting semantics.
- `pkg/knowl/app` owns business policy and consumer-side ports. Adapters depend
  on it; it does not depend on adapters. It may depend on `pkg/knowl/wiki`
  where business policy needs to reason about canonical markdown semantics.
- Adapters (`content/fs`, `store/*`, `provider`, MCP, HTTP, bootstrap) may
  depend on `app`, `types`, and `wiki` as needed, but not on each other unless
  they are part of the same surface-support package.
- `pkg/knowl` is the only package allowed to compose multiple adapters together.
- `pkg/knowlfx` and `cmd/knowl` are entrypoints. They may depend on
  `pkg/knowl`, but they should not become a second composition root with their
  own business rules.

## Granularity rule

When code becomes too large, prefer splitting files inside the same package
before creating a new package.

Create a new package only when at least one of these is true:

- it is a distinct external usage surface;
- it is a distinct adapter to an external technology boundary;
- it is a shared semantic contract needed by multiple layers.

Do not create a new package merely to:

- shorten one file;
- prepare for hypothetical reuse;
- turn support code for one surface into a fake standalone subsystem.

## Ownership

- Canonical content ownership lives in `pkg/knowl/content/fs`.
- Operational durability, leases, and projections live in `pkg/knowl/store/*`.
- Model planning lives behind `pkg/knowl/provider`.
- Business workflow and validation live in `pkg/knowl/app`.
- Transport-specific request and response mapping lives in the transport
  surface, not in `app`.

This keeps policy changes local to `pkg/knowl/app`, adapter swaps local to the
corresponding adapter package, and runtime wiring local to `pkg/knowl`.

Both MCP and HTTP expose the same three business operations: retrieve, ingest,
and operation status. Sidecar/service deployment is the baseline runtime shape;
`pkg/knowlfx` remains the in-process alternative over the same app core.

## Enforcement

`.go-arch-lint.yml` encodes only the top-level boundaries above. It is
deliberately conservative: it protects the main surfaces and inward dependency
flow without turning every subdirectory into a separate architectural concept.

See [workspace semantics](workspace.md), [service operations](operations.md),
the [product requirements](requirements.md), and
[integration boundaries](integrations.md).
