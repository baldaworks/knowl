# ADR 0003: Public full-page navigation through REST and MCP

- Status: Accepted
- Date: 2026-11-27
- Approved: 2026-11-27
- Owners: Knowl maintainers
- Decision scope: public full-page navigation through REST and MCP
- Supersedes: none
- Related: ADR 0001 defines scope selection and authorization; Prism Story
  `knowl-1ko` designs the `GetPage` use case

## Context

Knowl maintains canonical semantic pages as portable OKF Markdown. Public
retrieval provides compact page evidence, source references, resolved source
metadata, and OKF metadata when present. It does not provide an ordinary public
operation to read a complete semantic page. The privileged Operator API has
broader inspection capabilities and a different authorization boundary.

An agent can use retrieval to discover relevant pages, but needs a separate,
bounded operation to read one page in full and navigate its immediate links.
That operation should preserve the metadata already on the page without
changing how provenance is produced or interpreted.

## Decision

Knowl will introduce one shared application use case for public page
navigation:

```go
GetPage(ctx, GetPageRequest) (GetPageResult, error)
```

REST and MCP expose the same use case:

```text
REST  GET /v1/pages/{page_id}?scope=<name>
MCP   knowl_page(page_id, scope?)
```

Search and retrieve remain unchanged. A caller may use a page ID returned by
retrieval to request the complete page, then use the returned navigation
descriptors to select another page.

### Request and identity

The request identifies one page within a trusted, resolved scope:

```go
type GetPageRequest struct {
	Scope  ScopeRef
	PageID PageID
}
```

`PageID` is opaque at the application boundary and independent of the storage
implementation. Clients must use an issued ID rather than construct one from
a filesystem path or assume a particular encoding. This decision does not
change the current canonical storage model or promise a new identity
migration.

The REST facade must decode the path parameter unambiguously and reject invalid
or ambiguous forms before lookup. Caller input must not be used directly as a
filesystem path.

### Result and bounds

The result describes exactly one page and contains:

- its page ID, title, digest or version, and complete Markdown;
- OKF metadata already stored on the page, including `sources[]` and unknown
  extensions when present;
- available provenance and source descriptors, without source content;
- one-hop incoming and outgoing navigation descriptors;
- snapshot identity when the underlying read supports it; and
- an untrusted-content marker.

Navigation descriptors identify immediate relationships; they do not include
complete related pages or recursively expand the graph. Provenance descriptors
do not grant access to source content or imply that a source has been verified
or is trustworthy. This operation neither creates missing OKF provenance nor
normalizes it against Knowl source references.

Markdown asset links are returned unchanged and are never dereferenced by
`GetPage`.

The existing canonical page ceiling remains in force. In addition, the complete
serialized application result, including Markdown, metadata, provenance, and
navigation, is limited to 4 MiB (4,194,304 bytes). If it exceeds that limit,
the operation fails explicitly. It does not truncate the page, metadata, or
navigation, and it does not return a partial result. REST and MCP map the same
application outcome; transport-specific wrapping does not change the
application-result limit.

Where a snapshot identity is supplied, it must identify the read represented
by the result. The operation must not present independently observed data as
belonging to a snapshot it cannot establish.

### Scope and authorization

The facades reuse the existing scope resolution and read authorization
boundary. An optional public scope selection is resolved to trusted scope
context, and the principal is authorized for read access before page data is
read. Omission uses the configured default scope. A page ID or provenance
descriptor is not an authorization capability and must not allow a read across
scope boundaries.

REST and MCP map invalid requests, unauthorized scope access, missing pages,
unavailable reads, and oversized results consistently to their respective
transport errors.

### Operator API

The existing Operator API remains privileged and compatible. This decision
does not make its catalog browsing, source listing, operation history, or raw
inspection public.

### Out of scope

This decision does not add:

- `GetSourceRevision`, raw source reads, or `read_source` authorization;
- provenance normalization or dual-provenance generation;
- asset resolution, asset bytes, MCP image blocks, or `GetAsset`;
- OCR, captions, or other media-specific behavior;
- public catalog browsing, recursive navigation, or pagination; or
- the database-first storage, migration, and export changes in paused
  ADR 0005.

## Consequences

### Positive

- Agents can follow retrieval with a complete page read and bounded one-hop
  navigation.
- REST and MCP share one application contract and read policy.
- Existing OKF metadata and provenance descriptors remain available without
  changing their production or authority.
- Opaque page IDs keep the public application contract independent of storage
  representation.

### Costs and risks

- A valid page may fail to return when its complete serialized result exceeds
  4 MiB, including because of metadata or navigation descriptors.
- Clients must handle explicit size errors and cannot rely on partial results
  or pagination.
- One-hop navigation and provenance must be assembled consistently within the
  selected scope, with a snapshot identity only when supported.
- Returned Markdown and metadata are untrusted content; consumers must not
  treat the marker or a source descriptor as an attestation.

## Rejected alternatives

### Inline complete pages in retrieve

Inlining complete pages couples search ranking to content delivery and can
make retrieval responses substantially larger. A separate read lets callers
select the pages they need while leaving retrieval unchanged.

### Reuse the Operator API directly

Operator endpoints have privileged authorization and broader inspection
semantics. Public agents need a scope-aware application read contract.

### Add a universal navigation method with modes

A method combining search, page reads, source reads, neighbors, and catalog
browsing would couple distinct responsibilities and limits. This decision
addresses the immediate need with one focused page read.

## Delivery

The shared `GetPage` application contract is exposed through REST and MCP using
the existing scope resolution and read authorization boundary. It preserves the
complete page and metadata, returns one-hop incoming and outgoing navigation,
uses the page digest as its page-scoped version, and fails explicitly when the
serialized common result exceeds 4 MiB.
