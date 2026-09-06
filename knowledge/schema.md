# Knowl project wiki policy

schema_version: 1

## Purpose and enforcement boundary

This operator-owned Markdown policy guides the maintainer as untrusted input.
Maintainer plans may read it but may not modify it. Knowl code independently
enforces OKF structure, safe paths, provenance, links, limits, and protected
control files.

## Knowledge model

- Use `concepts/` for durable product architecture, workspace semantics,
  operations, deployment, and release behavior.
- Use `entities/` only for stable named components that need their own identity.
- Use `syntheses/` for conclusions supported by multiple documentation areas.
- Organize catalogs by product subject, not by the `docs/` source layout.

## Metadata and provenance

Preserve exact CLI commands, configuration keys, public identifiers, versions,
and limits from the cited documentation. Every factual page must retain stable
`knowl.source_refs`; merge related documentation into semantic pages rather than
creating one wiki page per source file.

## Links and organization

Link only to existing or same-plan pages, and keep every page reachable from the
root catalog. Prefer stable subject-oriented names when updating existing pages.

## Change handling

Describe the current public contract separately from historical release facts.
When documentation conflicts, retain the supported current behavior, record the
contradiction when it matters, and label superseded behavior explicitly instead
of silently merging incompatible claims.
