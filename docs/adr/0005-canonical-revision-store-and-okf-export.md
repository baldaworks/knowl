# ADR 0005: Make a revision store canonical and publish OKF as an export

- Status: Proposed
- Date: 2026-11-27
- Approved: pending
- Work state: paused by operator on 2026-11-27
- Owners: Knowl maintainers
- Decision scope: canonical knowledge storage, identity, revisions, assets,
  projections, and OKF publication
- Supersedes: none while proposed
- Related: ADR 0001, ADR 0002, ADR 0003, and ADR 0004

## Context

Knowl currently treats the `wiki/` OKF bundle as canonical knowledge. SQL is
rebuildable operational state and search projection. Source maintenance stages
complete Markdown files, commits them through filesystem preimages and recovery
receipts, and then rebuilds database projections.

That model is portable and Git-friendly, but it couples knowledge identity,
transactions, navigation, and recovery to filesystem paths. A large corpus can
place tens of thousands of concepts in one directory. Physical sharding changes
standard OKF concept IDs because OKF derives an ID from the bundle-relative
file path. Full workspace walks, complete hierarchy reconciliation, large
`index.md` files, and projection rebuilds also become material scale limits
before the filesystem itself necessarily fails.

The product does not require direct editing of canonical wiki files. Future
human changes may be proposed and committed through application use cases. A
filesystem bundle may therefore be a published, read-only representation rather
than runtime authority.

Multimodal knowledge must remain first-class in this model. Images and other
supported assets are immutable evidence associated with source and page
revisions, not incidental files that exist only because Markdown links to them.

This ADR records a future direction only. It authorizes no implementation,
migration, API change, or change to the current file-canonical contract.

## Proposed decision

Replace the canonical OKF workspace with a canonical revision store. Treat OKF
as a deterministic import, export, and publication format.

```text
sources and assets
        |
        v
maintainer candidate
        |
        v
canonical Knowledge Store
  - stable page identities
  - immutable page revisions
  - provenance and source bindings
  - links and catalog membership
  - immutable asset bindings
        |
        +--> lexical and vector projections
        +--> REST, MCP, and UI read models
        +--> deterministic OKF export
```

`wiki/` MUST NOT be accepted as live runtime state or an implicit write path.
Generated OKF bundles are read-only artifacts. Configuration and `schema.md`
remain operator-managed inputs, but knowledge changes occur only through
application operations.

### Canonical identity and revisions

Pages have opaque, immutable internal identities independent of slugs, catalog
placement, and export paths. Slugs and display titles may change without
changing the page identity.

Page revisions are immutable. A successful change creates a new revision and
atomically advances the page's current-revision pointer. Expected revision,
not a filesystem preimage, provides optimistic concurrency control.

A representative logical model is:

```text
pages
  id
  current_revision_id
  slug
  created_at

page_revisions
  id
  page_id
  revision
  body_markdown
  metadata
  content_digest
  created_by_operation
  created_at

page_sources
page_links
catalogs
catalog_memberships
```

The final SQL schema, ID encoding, and revision-number representation remain
design decisions. Metadata must remain structurally representable as OKF and
must preserve unknown OKF extensions.

### Application commit boundary

A knowledge commit is one application transaction:

1. authenticate and validate the operation and expected revisions;
2. validate semantic metadata, provenance, links, and asset bindings;
3. create immutable page revisions;
4. update current page revisions, links, and catalog membership;
5. record the committed operation outcome; and
6. enqueue idempotent projection and export work through a transactional
   outbox or equivalent durable mechanism.

Once this transaction commits, the knowledge change is committed. Search,
vector, thumbnail, OCR, caption, and OKF export failures do not roll back the
canonical revision. They are explicit lagging projections that can be retried
from immutable canonical inputs.

This direction is expected to remove the need for canonical filesystem staging
directories, file preimages, filesystem commit receipts, and file-to-SQL
recovery. Exact replacement and migration of those mechanisms requires a later
design and implementation plan.

### Internal links

Internal page and asset links refer to stable identities rather than export
paths. One possible internal Markdown representation is:

```markdown
[Related page](knowl://page/<page-id>)
![Architecture](knowl://asset/<asset-id>)
```

The exact internal URI syntax remains open. Public readers and exporters must
resolve these identities under scope authorization. An OKF exporter rewrites
them to relative bundle paths for the selected export layout.

### Canonical multimodal assets

Binary asset bytes belong to a content-addressed canonical `AssetStore`, not to
the OKF exporter. SQL stores immutable identity and metadata; the blob store
stores bytes keyed by digest.

```text
assets
  id
  digest
  media_type
  byte_size
  dimensions
  blob_key

source_revision_assets
  source_revision_id
  asset_id
  relation
  original_ref
  alt_text

page_revision_assets
  page_revision_id
  asset_id
  role
  alt_text
  ordinal
```

A local deployment may use a content-addressed directory. A service deployment
may use an object store. Blob publication is idempotent: bytes are validated and
written before a database transaction references them. Unreferenced blobs are
eligible for bounded garbage collection; blobs referenced by any retained
source revision, page revision, or export snapshot remain live.

Maintenance resolves supported local embedded source assets to immutable asset
IDs, applies byte, count, media-type, and pixel bounds, and supplies a fixed
asset set to the multimodal provider. Correction turns reuse that exact set.
External URLs are never fetched implicitly.

OCR, captions, image embeddings, and thumbnails are rebuildable projections.
They record their producer/model fingerprint and are not canonical evidence.
Human- or source-supplied alt text and immutable asset bindings are canonical.

The initial multimodal implementation should consume source-derived assets.
Whether maintainers may generate new assets remains an open decision.

### OKF export

An export is a versioned materialized snapshot of one committed canonical
revision boundary. Its identity includes at least scope, canonical snapshot,
and layout version.

```text
export_snapshots
  id
  scope
  canonical_snapshot
  layout_version
  state
  digest
  published_at
```

For the same canonical snapshot and layout version, export must be byte-for-byte
deterministic. The exporter:

1. captures one consistent canonical snapshot;
2. assigns deterministic bundle paths to pages, catalogs, and assets;
3. renders complete OKF frontmatter and Markdown;
4. rewrites internal page and asset identities to relative OKF paths;
5. renders bounded hierarchical `index.md` files;
6. materializes referenced assets under deterministic digest-based paths;
7. validates the complete OKF bundle; and
8. publishes it atomically to a directory, archive, Git target, or object
   storage destination.

Export path layout does not define internal page identity. Layout versions may
use semantic directories, deterministic hash sharding, or another bounded
strategy. Changing a layout version must not mutate canonical pages or links.

An export failure leaves canonical knowledge unchanged. A retry from the same
snapshot must reproduce the same output. Export may be requested explicitly,
performed for a release, or debounced after a group of knowledge commits; it is
not required after every page revision.

### Retrieval and navigation

REST, MCP, UI, lexical search, vector retrieval, and hierarchy operations read
the canonical store and derived projections, not exported files. Catalogs are
stored as a graph and are not limited by the byte size or link count of one
generated `index.md`. The exporter decomposes a large graph into bounded OKF
indexes.

`GetPage` returns page metadata, Markdown, provenance, links, and compact asset
descriptors for one canonical revision. Asset bytes are read through a separate
authorized `GetAsset` or source-revision operation with media and response
bounds. Retrieval may fuse text, metadata, OCR/caption, and image-vector
channels without making derived captions canonical.

### Import and direct editing

Generated OKF output is read-only. Knowl will not provide transparent two-way
filesystem synchronization or treat an edited export as a canonical commit.

The proposed narrow import roles are:

- bootstrap an empty scope from an existing OKF bundle; and
- restore a Knowl-produced bundle when sufficient stable identity and revision
  metadata is present.

Whether arbitrary OKF import may update a non-empty scope is unresolved. If
supported later, it must be an explicit, authorized operation with conflict
detection, not directory watching or implicit round-trip synchronization.

## Migration direction

A future migration would import the current canonical `wiki/` and immutable
raw history into the revision and asset stores, preserve current page IDs as
legacy identity metadata where possible, rebuild links and catalogs, and verify
a deterministic exported bundle before changing runtime authority.

Migration must be resumable, idempotent, and reversible until cutover. It must
define backup, rollback, dual-write prohibition, projection rebuilding, and
SQLite/PostgreSQL parity. No migration design is approved by this ADR while it
remains Proposed.

If accepted, this decision will supersede the file-canonical storage assumption
in existing architecture documentation and the relevant parts of ADR 0001. It
will not change ADR 0001's scope-isolation guarantees. ADR 0002 and ADR 0003
must be reconciled with canonical asset IDs and database-backed page navigation
before implementation. Public source navigation remains a separate future
decision.

## Consequences

### Positive

- Stable identities no longer depend on physical OKF paths.
- Page, provenance, link, catalog, and asset changes can commit atomically.
- Search and export can update incrementally from immutable revisions.
- Large corpora do not require a canonical directory containing tens of
  thousands of files.
- Catalog size and semantic placement no longer constrain canonical storage.
- Multimodal evidence becomes a first-class revision-bound resource.
- Multiple deterministic export layouts can serve different publication needs.

### Costs and risks

- The directly inspectable Markdown workspace is no longer the source of truth.
- Backup and disaster recovery must cover both the canonical database and blob
  store.
- SQLite and PostgreSQL need equivalent revision, transaction, and outbox
  semantics.
- Export freshness becomes an observable operational state.
- Internal identity links require resolution and export rewriting.
- Existing filesystem recovery and tests require substantial replacement.
- Migration changes a foundational product guarantee and will require staged
  compatibility and release planning.
- An OKF export alone may not retain all historical revisions unless the export
  contract explicitly includes them.

## Alternatives considered

### Keep OKF canonical and shard concept directories

This reduces directory fan-out but leaves path-derived identity, full workspace
walks, filesystem transaction machinery, and complete hierarchy limits. It is a
smaller change and remains viable if direct Git editing is later required.

### Maintain both file-canonical and database-canonical modes

Rejected as the target direction. Two authorities require different commit,
recovery, conflict, and identity semantics and risk long-term behavioral drift.

### Store mutable current pages without immutable revisions

Rejected. Mutable rows weaken replay, audit, deterministic export, correction,
and recovery. Current pointers over immutable revisions provide a clearer
authority boundary.

### Allow transparent export editing and re-import

Rejected as a default workflow. It recreates filesystem conflict semantics and
makes authority ambiguous. Any future import into a live scope must be an
explicit operation.

### Store binary assets inside page rows

Rejected. Content-addressed blobs provide deduplication, streaming, independent
bounds, and storage-backend flexibility while revisions retain immutable asset
bindings.

## Open decisions

- Canonical database and blob-store implementations for local and service
  deployments.
- Stable page-ID and revision-ID encodings.
- Internal page/asset URI representation.
- Exact transaction and outbox boundary across operations and projections.
- Export layout versions and compatibility guarantees.
- Whether import may update a non-empty scope or is limited to bootstrap and
  disaster restore.
- Retention and garbage-collection policy for historical revisions and blobs.
- Whether maintainers may create derived assets or only bind source assets.
- Migration and cutover plan from file-canonical workspaces.

## Pause condition

Architecture and implementation work for this proposal are intentionally
paused. Resume only on an explicit operator request. Before implementation, the
open decisions require a follow-up design, ADR review, migration plan, and
approved Prism initiative.
