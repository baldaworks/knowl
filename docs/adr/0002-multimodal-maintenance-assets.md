# ADR 0002: Supply embedded source assets to multimodal maintainers

- Status: Proposed
- Date: 2026-11-27
- Approved: pending
- Owners: Knowl maintainers
- Decision scope: source asset resolution, maintenance input, provider
  capabilities, provenance, and input safety
- Supersedes: none
- Related: ADR 0003 defines public page navigation; public source/asset reads
  and portable provenance require a separate decision

## Context

Knowl sources may contain Markdown pages that refer to auxiliary assets. The
filesystem source adapter already inventories Markdown and non-Markdown files,
accepts immutable byte-exact revisions, records media types, and mirrors assets
into the canonical source tree. Obsidian asset embeds are normalized to ordinary
relative Markdown image references.

Source maintenance is nevertheless text-only. `MaintenanceInput` contains the
current document as `SourceText`, and the runtime provider serializes the input
into one textual user message. A maintainer can see an image path and its alt
text but cannot inspect the referenced image. Durable facts represented only in
diagrams, screenshots, or other visual material are therefore unavailable to
semantic maintenance.

Passing every source asset to every maintenance request would be expensive and
would weaken request bounds. Fetching arbitrary remote references would also
make maintenance nondeterministic and introduce network and SSRF risks. Asset
selection must remain tied to the immutable accepted source revision and must
be reproducible during validation and correction turns.

## Decision

Knowl will add bounded multimodal evidence to source maintenance. The first
version will attach supported local images that are embedded directly by the
current Markdown document. It will not fetch remote URLs, recursively inspect
linked documents, or attach every asset owned by a source.

The maintenance flow will be:

```text
accepted Markdown revision
  -> resolve asset references against the immutable source inventory
  -> build a deterministic asset manifest
  -> select supported embedded images within configured bounds
  -> construct one multimodal maintainer request
  -> validate edits and asset provenance
```

### Asset identity and resolution

An asset reference is owned by one accepted source document revision and is
identified independently of a mutable filesystem path:

```go
type AssetRef struct {
	SourceID   SourceID
	DocumentID DocumentID
	Revision   string
	Path       string
	Digest     string
	MediaType  string
}
```

Relative references are resolved from the containing Markdown document against
the immutable inventory produced by the same source scan. Resolution must use
the source adapter's containment, symlink, and revision checks; the maintainer
must not read arbitrary workspace paths.

Only Markdown image embeds such as `![architecture](assets/system.png)` are
automatically eligible in the initial contract. Remote URLs remain references
only. Missing, ambiguous, unsupported, or rejected assets are represented in
the manifest with a bounded diagnostic rather than silently fetched or
interpreted from alt text.

Whether an ordinary link such as `[diagram](assets/system.png)` should make the
target eligible is intentionally unresolved. Until that decision is approved,
ordinary links are recorded in the manifest but are not attached.

### Maintenance contract

`MaintenanceInput` will gain a deterministic asset manifest. Selected entries
carry immutable content separately from the JSON/textual contract:

```go
type MaintenanceAsset struct {
	Ref      AssetRef
	Relation string // embedded or linked
	AltText  string
	Content  []byte // present only for a selected attachment
}
```

The serialized maintenance envelope describes each reference, selection
outcome, and diagnostic. The provider adapter converts selected content into
native multimodal message parts alongside the textual maintenance contract.
Binary content is not base64-expanded inside `SourceText` or page snapshots.

The same normalized manifest and selected immutable bytes are reused for every
correction turn of one maintenance operation. Corrections must not re-resolve
mutable paths or obtain a different asset revision.

Provider configuration or capabilities must declare whether multimodal input
is supported and which media types it accepts. If the selected provider cannot
accept an otherwise eligible asset, maintenance records an explicit bounded
`asset_not_processed` diagnostic. Knowl must not imply that the model inspected
the image merely because its path or alt text was present.

### Initial media and resource bounds

The initial supported media set is PNG, JPEG, and WebP. Media type is verified
from bounded content rather than trusted solely from a filename or adapter
metadata.

Selection is bounded independently by:

- maximum attachments per maintenance operation;
- maximum encoded bytes per asset;
- maximum total attachment bytes;
- maximum decoded pixel dimensions and pixel count; and
- the complete provider request budget.

Selection order is deterministic and follows first appearance in the Markdown
document, with duplicate references to the same immutable asset attached once.
An asset that would exceed a bound remains visible in the manifest with a
stable rejection reason.

SVG, PDF, audio, video, archives, and unknown binary media are not attached in
the first version. SVG is excluded because it mixes visual content with active
or external-resource-capable markup. PDF and time-based media require separate
rendering, sampling, and cost policies.

Decoders must reject malformed content and decompression bombs before provider
invocation. Metadata not required for interpretation, including location and
device metadata, should be removed or excluded from provider input where the
format permits deterministic normalization.

### Provenance

Facts derived from an attached image retain the parent accepted source
reference and the immutable `AssetRef`. Asset evidence does not become an
independent mutable source and does not weaken the requirement that edited
pages cite the accepted source revision.

Canonical pages require a future provenance decision to reconcile two distinct
needs:

- standard OKF `sources[]` records a portable bundle-relative reference to the
  mirrored asset and provides an optional stable `id` for claim-level Markdown
  footnotes;
- `knowl.source_refs` retains the exact accepted revision authority used by
  reconciliation and retirement; and
- a namespaced Knowl extension binds the portable source entry to immutable
  asset digest, revision, and media type without exposing a host filesystem
  path.

Facts derived from an image cite both the containing Markdown revision and the
asset revision. Retirement and reconciliation remove asset-derived facts when
either required supporting revision is no longer active. Credibility,
verification, and authorship fields are not inferred from an asset path or
model inspection.

### Public reading and retrieval

This decision concerns maintenance input. It does not define public page,
source, or binary delivery. ADR 0003 separately defines `GetPage`; public raw
source or asset reads and their authorization require another decision.
Retrieval does not inline asset bytes, and this ADR does not add recursive
graph expansion.

## Consequences

### Positive

- Maintainers can synthesize facts contained in diagrams and screenshots.
- Asset evidence remains immutable, bounded, reproducible, and source-scoped.
- Existing byte-exact source inventory and mirroring provide the storage
  foundation without remote fetching.
- Provider limitations and skipped assets remain explicit rather than silently
  degrading to alt-text interpretation.
- The contract can later add other media types without changing Markdown into
  an unbounded binary container.

### Costs and risks

- Maintenance input assembly must parse references and join them to the exact
  accepted source inventory.
- Provider adapters need capability-aware multimodal message construction.
- Image validation, decoding bounds, metadata handling, and request accounting
  add security-sensitive code.
- Asset-level provenance and retirement semantics extend the current
  document-level provenance model.
- Multimodal inference increases provider cost and may produce different
  results across models even for identical inputs.

## Rejected alternatives

### Attach every source asset eagerly

Most assets are unrelated to the current document. Attaching them all makes
cost and request size depend on source inventory rather than evidence selected
by the document and can exceed provider limits before maintenance begins.

### Fetch remote Markdown URLs during maintenance

Remote content is mutable and introduces SSRF, credential leakage, latency,
and availability risks. A remote asset must enter through an explicit source
adapter that produces an immutable accepted revision before it can be evidence.

### Encode binary assets inside `SourceText`

Base64 expansion consumes the textual request budget, obscures media type and
capability handling, and prevents providers from using native multimodal
message parts.

### Rely only on OCR or generated captions

Text derivatives are useful for search and text-only providers but lose visual
structure and introduce another model-derived evidence layer. They may be
added as an explicit derived artifact, not treated as equivalent to the
original asset.

### Let the maintainer fetch arbitrary assets through a tool

On-demand tools can reduce eager attachment cost, but they expand the model's
read authority and make the exact evidence set harder to reproduce. A bounded
asset-reading tool may be considered later if eager selection proves
insufficient.

## Open questions

1. Should ordinary local links to supported images be attached, or should only
   Markdown image embeds be eligible?
2. What concrete defaults should apply to asset count, individual bytes, total
   bytes, dimensions, and pixel count?
3. Should unsupported assets fail the maintenance operation under a strict
   policy, or always degrade with an explicit diagnostic?
4. What stable OKF `sources[].id` convention should identify an asset for
   claim-level footnotes?
5. Should deterministic OCR or captions be stored for lexical retrieval, and
   if so, are they derived state or canonical evidence?

## Delivery outline

1. Define immutable asset references, manifest diagnostics, media validation,
   and independent resource limits.
2. Resolve Markdown embeds against the accepted source inventory and assemble
   deterministic maintenance assets without changing text-only behavior.
3. Add provider capability discovery and native multimodal request parts, with
   an explicit non-multimodal fallback diagnostic.
4. Extend request accounting, correction replay, provenance validation,
   retirement, and operation diagnostics.
5. Add security and behavioral coverage for traversal, symlinks, remote URLs,
   MIME mismatch, malformed images, decompression bombs, duplicate references,
   deterministic selection, request limits, provider fallback, and correction
   reuse.
6. Document configuration, supported media, costs, diagnostics, and operator
   recovery behavior before accepting this ADR.
