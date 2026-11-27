# ADR 0006: Return compact evidence from public retrieve

- Status: Accepted
- Date: 2026-11-27
- Approved: 2026-11-27
- Owners: Knowl maintainers
- Decision scope: public REST and MCP retrieval result shape and response bounds
- Supersedes: none
- Related: ADR 0003 provides full-page continuation by page ID

## Context

Public retrieval currently uses the general query result assembled by
`QueryService`. The default read limits permit 20 pages and up to 262,144
characters of evidence per page. REST and MCP expose rich page metadata,
source documents, OKF metadata, and a separate aggregate citation list.

Those bounds are useful for richer internal reads but excessive for a search
entry point. A measured query whose first result already contained enough
evidence to answer the question produced a 433,741-byte tool result: 20 pages,
371,467 bytes of snippets, and 81 citations. One snippet alone contained
27,842 characters. The ranking was useful, but delivery spent model context on
pages the consumer had not selected.

Accepted ADR 0003 introduces `GetPage` through public REST and MCP. That
operation is implemented. It lets a consumer read a selected page’s complete
Markdown, metadata, provenance, and one-hop navigation. Retrieval can
therefore identify and briefly support candidate pages while leaving complete
page reading to that separate operation.

## Decision

After public `GetPage` is available, public REST and MCP retrieve will return a
compact, shared application result. The request retains the existing `query`,
`scope`, and optional `sources` filter semantics, including scope resolution
and read authorization. Search and hybrid ranking remain responsible for
selecting and ordering pages; this decision does not change ranking.

The initial public limits are:

- at most three ranked page matches;
- at most 800 Unicode code points in each snippet; and
- at most 8 KiB (8,192 bytes) in the serialized common application result.

Each match contains only:

```json
{
  "page_id": "entities/balet-vesna-v-ala-too",
  "title": "Балет «Весна в Ала-Тоо»",
  "snippet": "«Весна в Ала-Тоо» — балет Владимира Власова и Владимира Фере на либретто П. Аболимова, появившийся в 1955 году.",
  "citation": "knowl://sources/open-kg/open-kg/art/ballet/35185.md",
  "untrusted": true
}
```

`page_id` is the stable, opaque ID issued for the page and accepted by
`GetPage` in the same authorized scope. Clients must not construct IDs or
assume a storage encoding.

`citation` is optional and identifies at most one supporting reference already
eligible for public retrieval. It is named separately from the request’s
`sources` filter and does not claim to contain the page’s complete provenance.
When several eligible references exist, the application selects one
deterministically, preferring direct accepted-source evidence over a redundant
wiki-page citation. A citation is a descriptor, not an authorization
capability or an attestation of trust. `GetPage` remains the operation for
complete stored provenance descriptors.

Every card marks its content as untrusted. The result may additionally carry
the existing bounded retrieval-mode status and normalized query needed by
clients. It does not include full page metadata, source-document arrays, OKF
metadata, link arrays, complete provenance, or an aggregate citation array.

### Extractive snippets

A snippet is untrusted evidence copied from indexed page material. Retrieve
does not ask a model to answer, summarize, or rewrite the query.

For a body match, the application selects a Unicode-safe window around the
actual matched passage or dense evidence chunk. The window is not anchored to
the beginning of the page, so a match near the end remains visible. For a
title, description, or tag-only match, the snippet uses the corresponding
matched stored field and identifies that evidence without fabricating body
text. Selection and clipping are deterministic for the same index snapshot,
query, and limits. Clipping must not split a UTF-8 encoding, and the
800-code-point limit applies to the returned snippet.

### Aggregate response budget

The application serializes the common result in the canonical REST/MCP DTO
shape and measures its complete UTF-8 JSON byte length, including the query,
retrieval status, identifiers, snippets, citations, escaping, and JSON
structure. A result of exactly 8,192 bytes fits.

Results are considered in rank order. If the serialized result exceeds
8,192 bytes, the lowest-ranked complete card is removed and the result is
serialized and measured again. This continues until the result fits. The
application never shortens required identifiers, silently cuts JSON, or
substitutes a lower-ranked page for a higher-ranked one.

If the required envelope and highest-ranked bounded card cannot fit, retrieval
fails without a partial result using a stable, machine-readable
response-limit error. If there are no matches and the required envelope alone
cannot fit, the same explicit error applies.

REST and MCP map the same application result and response-limit outcome to
their respective transport contracts. MCP emits the structured result once and
only a constant-size text notice; it does not duplicate the JSON or snippets
in a second text representation. Transport envelopes are outside the 8 KiB
application budget but remain subject to their own limits.

### Rollout and compatibility

Compact retrieve depends on public page reading. Deployment and client
migration follow this order:

1. Ensure ADR 0003 `GetPage` is deployed and documented for REST and MCP.
2. Validate compact retrieval against representative production captures.
3. Publish the compact schema and migration notes in an explicitly breaking
   public-contract release.
4. Switch public retrieve to the compact result.

Clients that need complete page content or provenance must follow a returned
`page_id` with `GetPage` in the same authorized scope. There is no long-lived
compatibility mode that keeps the oversized public response. Internal search,
maintenance context selection, operator inspection, and evaluation APIs may
retain richer results and independent limits; this ADR changes only public
REST and MCP retrieval.

### Out of scope

This decision does not change ranking algorithms, embeddings, source-filter
semantics, `GetPage`, raw-source access, catalog browsing, generated answers,
or page storage. It does not add multimodal content, asset bytes, OCR,
captions, or media-specific retrieval. It does not define user-selectable
public result or snippet limits in the initial contract.

## Consequences

### Positive

- A search call consumes a small, predictable amount of tool and model
  context.
- Stable page IDs let consumers choose when complete content is worth loading.
- Snippets preserve evidence near the actual match instead of returning a page
  prefix or an oversized page body.
- A single optional citation supports the compact evidence without presenting
  an incomplete list as full provenance.
- REST and MCP share ranking, clipping, and response-budget policy.

### Costs and risks

- Questions requiring details outside the snippet need a second `GetPage`
  call.
- Three results may reduce recall for ambiguous or exploratory queries.
- Existing clients that depend on rich evidence fields require a breaking
  migration.
- Deterministic snippet selection must remain consistent across lexical and
  dense retrieval implementations.
- JSON measurement adds serialization work, although the small result bound
  keeps that cost bounded.
- An unusually large required envelope or first card can produce an explicit
  response-limit error even when a page matches.

## Rejected alternatives

### Keep the current rich retrieve result and only lower page limits

Rich metadata and aggregate citations can still consume most of the response
budget, while retrieval and full-page reading continue to overlap.

### Return the first 800 characters of every page

Relevant evidence may occur near the end of a page. Prefix clipping would
preserve size but lose the passage that caused the match.

### Generate a short answer in retrieve

Generation adds inference cost, latency, nondeterminism, and a new trust
boundary. Retrieve is an evidence-selection operation; the consuming agent can
form an answer from the extractive snippet or read the page.

### Return every citation for each match

Complete provenance is useful during page inspection but recreates the
response-growth problem at the search boundary. One optional supporting
citation is sufficient for the compact card.

### Silently truncate the serialized response at 8 KiB

Byte truncation can produce invalid JSON or partial identifiers and makes the
result dependent on transport encoding. Rank-aware removal preserves complete
cards and fails explicitly when even the highest-ranked card cannot fit.

## Validation

Compare existing and compact public results on a fixed corpus containing:

- a direct factual question answered by the first match;
- a match near the end of a long page;
- a question whose detail requires `GetPage`;
- a tag, title, or description-only match; and
- a query with no results.

Verify first-result relevance and unchanged ranking, extractive evidence near
the actual match, deterministic citation selection, untrusted markers,
source-filter and scope isolation, and successful continuation from `page_id`
to `GetPage` in the same authorized scope. Exercise both public transports and
verify equivalent common results and errors, including that MCP does not
duplicate the structured payload as text.

Boundary tests cover multibyte Unicode and JSON escaping, snippets at and over
800 code points, application results at and over 8,192 serialized bytes,
removal of lowest-ranked complete cards, and stable failure when the required
envelope plus the first bounded card cannot fit. They also cover an oversized
empty-result envelope and confirm that no partial result is returned on
failure.
