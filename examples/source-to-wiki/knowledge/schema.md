# Acme engineering wiki policy

schema_version: 1

## Purpose and enforcement boundary

This operator-owned Markdown policy guides the maintainer as untrusted input.
Maintainer plans may read it but may not modify it. Knowl code independently
enforces OKF structure, safe paths, provenance, links, limits, and protected
control files.

## Knowledge model

- Use `entities/` for stable named systems and services in the Acme Cloud
  Platform.
- Use `concepts/` for cross-cutting security, data lifecycle, and incident
  response policies or procedures.
- Use `syntheses/` only for conclusions that genuinely combine multiple systems
  or policy areas.
- Organize catalogs by engineering subject rather than source filename.

## Metadata and provenance

Preserve exact protocol names, durations, retention periods, recovery steps, and
component relationships from the sources. Every factual page must retain stable
`knowl.source_refs`. Merge overlapping evidence into shared semantic pages
instead of mirroring one page per input document.

## Links and organization

Link only to existing or same-plan pages, and keep every page reachable from the
root catalog. Use plain text when a related page has not been confirmed.

## Change handling

Preserve compatible facts during updates. Record material contradictions and
identify superseded policy explicitly; do not silently choose between conflicting
source claims.
