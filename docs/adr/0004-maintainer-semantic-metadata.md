# ADR 0004: Require semantic metadata for maintained pages

- Status: Accepted
- Date: 2026-11-27
- Approved: 2026-11-27
- Owners: Knowl maintainers
- Decision scope: semantic metadata produced by ordinary source maintenance
- Supersedes: none
- Related: ADR 0003 proposes a separate portable provenance contract

## Context

Knowl maintains ordinary factual pages as portable OKF Markdown. Its source
maintainer receives an exact frontmatter shape that includes `type`, `title`,
`knowl.id`, and `knowl.source_refs`. That instruction does not require a
`description` or `tags`.

The rest of Knowl can use those semantic fields when they are present. OKF
parsing and projection support them; search can use projected metadata;
hierarchy maintenance receives descriptions and tags as input; and REST
metadata and the UI can expose them. Pages produced without descriptions or
tags therefore leave useful capabilities with incomplete input.

The maintainer returns complete replacement content for an edited page.
Requiring additional semantic metadata must not turn replacement into
permission to discard unrelated OKF fields, extensions, or existing Knowl
provenance.

OKF also supports fields such as verification and trust tier. Knowl has no
operation that writes verification, so that support is inert compatibility
rather than a Knowl verification or trust lifecycle.

## Decision

Ordinary source maintenance must produce the following metadata on every
ordinary factual page it creates or modifies:

- a nonempty `type`;
- a nonempty `title`;
- a concise, nonempty `description` describing the page’s subject; and
- one to six meaningful semantic `tags` relevant to the page’s subject.

Tags should help distinguish and discover the subject. A description or tag
must not be an empty or whitespace-only value. Conciseness and semantic
meaning are maintainer responsibilities; structural validation alone cannot
establish their quality.

The source maintainer’s exact frontmatter instruction will include
`description` and `tags` alongside the existing identity and provenance
requirements. Application validation will enforce the required fields and tag
count for ordinary factual page edits before publication. Violations enter the
existing source-plan correction path, subject to its existing limits and
failure behavior.

This is an output-affecting contract change. Knowl will bump the source
maintenance contract version and its resulting policy generation so queued or
resumed work cannot silently run under the old contract.

### Replacement and compatibility

For replacement edits, source maintenance must preserve existing OKF fields and
extensions outside the maintainer-owned semantic fields and existing Knowl
provenance, unless another already-authorized lifecycle explicitly owns a
change. The existing rules for source references, including authorized
replacement for the same source lineage, continue to apply. This decision
creates no new lifecycle for changing other metadata.

Existing imported, manually authored, and untouched canonical pages remain
compatible even when they lack a description or tags. No automatic backfill is
required. If ordinary source maintenance later modifies such a page, its
resulting replacement must satisfy this decision.

### Out of scope

This decision does not introduce verification or review operations, a trust
lifecycle, `status` or `stale_after` management, or generated metadata. It does
not convert `knowl.source_refs` into OKF `sources[]`; ADR 0003 separately
proposes a dual provenance model. It does not establish a global tag registry,
perform mass metadata reconciliation or backfill, add UI filtering or tag
pages, or change public APIs.

## Consequences

### Positive

- Newly maintained factual pages consistently provide semantic signals already
  supported by Knowl’s metadata consumers.
- Hierarchy maintenance has more subject context for pages as ordinary source
  maintenance touches them.
- Validation catches missing or structurally invalid metadata before
  publication, while the existing correction path gives the maintainer an
  opportunity to repair its output.
- Untouched and externally authored content remains compatible without a
  migration.
- Replacement edits retain metadata and provenance outside the authority of
  ordinary source maintenance.

### Costs and risks

- Maintenance requests may require correction or fail when the maintainer
  cannot produce conforming metadata within existing limits.
- A nonempty description and bounded tag list do not guarantee accurate,
  concise, or useful semantics. Tag vocabulary may vary across pages.
- Existing pages acquire the new metadata incrementally, so coverage remains
  uneven until they are maintained.
- Preserving unrelated fields and extensions requires care when validating
  complete replacement pages.
- The contract-version change requires compatibility handling for queued or
  resumed work under the existing maintenance-generation rules.

## Rejected alternatives

### Keep descriptions and tags optional

This preserves the current permissive output, but leaves hierarchy and other
metadata consumers dependent on fields the maintainer is not instructed to
produce.

### Require metadata on every canonical page immediately

A global requirement would make existing imported, manually authored, and
untouched pages dependent on a migration or backfill. Restricting the
requirement to ordinary source-maintenance edits improves coverage as pages
change while preserving compatibility.

### Add instructions without application validation

Prompt guidance alone can be omitted or misapplied by a maintainer. Validation
provides a publication boundary and uses the established correction path for
structural violations.

### Generate descriptions and tags in a separate reconciliation pass

A separate pass would add a metadata lifecycle and could rewrite pages
unrelated to the current source. Ordinary maintenance already has the page and
source context needed to supply these fields when it creates or changes a page.

### Establish a global tag registry

A controlled vocabulary could reduce variation, but it would add governance
and coordination beyond the current need. Meaningful bounded tags provide a
useful starting contract without making registry membership a publication
requirement.
