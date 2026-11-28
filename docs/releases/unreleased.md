# Unreleased

## MCP usage skill

Knowl now publishes one canonical usage skill for compatible MCP hosts through
the `io.modelcontextprotocol/skills` extension. `skills/list`, `skills/get` and
`resources/read` expose the repository-authored
[`SKILL.md`](../../internal/mcpsdk/skills/knowl/SKILL.md) with its complete
manifest, byte size and SHA-256 digest. Read-only and read-write facades serve
identical bytes; ingestion guidance remains conditional on `knowl_ingest`
appearing in ordinary tool discovery.

The same skill can be installed directly from Git through the root
[`skills.sh.json`](../../skills.sh.json), preferably from a pinned release tag
that contains the skill or, until such a tag exists, a full 40-character commit
SHA for a commit that contains it. Git installation does not configure or
authorize a Knowl MCP server, MCP discovery does not install host files, and
clients without Skills Extension support retain their existing tool behavior.
This skill is separate from the local Codex plugin's `$knowl:setup` and
`$knowl:run` maintenance workflows. See
[operations](../operations.md#mcp-usage-skill) for installation and trust
details.

## Compact public retrieve

`GET /v1/retrieve` and MCP `knowl_retrieve` now return the same compact result:
at most three ranked cards, each with a stable `page_id`, title, extractive
snippet of at most 800 Unicode code points, one optional citation and an
untrusted marker. The complete application JSON is limited to 8 KiB and MCP no
longer duplicates it in text content. Follow `page_id` with `GET
/v1/pages/{page_id}` or `knowl_page` for full content and provenance.

This intentionally replaces the old rich public response in place. There is no
legacy endpoint, tool, feature flag or parallel API version. Clients using
`source_refs`, `source_documents`, `okf` or aggregate `citations` from public
retrieve must migrate to the page-reading operation. The Operator Web UI keeps
its private rich search view.

## Configurable source inventory limits

The v0.9.0 feature release adds the optional per-source
`sources[].sync.max_documents` setting. It defaults to `1000` and accepts
integers from `1` through `50000`, inclusive, allowing one configured source to
synchronize an inventory larger than the previous `1000`-document complete-scan
limit without raising limits for other sources.

The descriptor page ceiling remains `1000`; larger inventories are accumulated
across multiple bounded pages. The normalized effective limit participates in
the source configuration digest, and SQLite and PostgreSQL retain the same
absolute `50000`-document durable-state ceiling. No database migration is
required.

Higher limits can increase synchronization memory, database work, and recovery
time. Before rolling back to a binary that predates this setting, remove
`max_documents` from source configuration. See
[operations and configuration](../operations.md#source-inventory-limits) for
the complete contract and a `max_documents: 10000` example.

## HTTP facade policy migration

API, Streamable HTTP MCP, and the optional Web UI now have independent
authentication and capability policy. `auth_mode` defaults to `token` when an
operator token exists and `public` otherwise. `access_mode` always defaults to
`read_only`.

This changes existing service behavior intentionally: a configuration that sets
`operator.token` but omits API/MCP `access_mode` still requires its bearer, but
ingestion is disabled after upgrade. Add the following only to facades that
must remain writable:

```yaml
knowl:
  api:
    auth_mode: token
    access_mode: read_write
  mcp:
    auth_mode: token
    access_mode: read_write
```

Public write configurations are invalid. Back up the workspace and operational
database before upgrade, keep the origin private when using an identity-aware
proxy, and verify retrieve, operation and ingest behavior for every enabled
facade. See [HTTP facade access control](../access-control.md) for deployment
profiles and the Pomerium discovery limitation.

## Database statistics page

The optional Web UI now includes a read-only **Statistics** page. Each open or
manual refresh reads the current database state; Knowl does not persist the
reading, retain history, poll, or render graphs. Scope-bound product counts are
shown separately from deployment-wide database and fixed-table storage facts.

SQLite and PostgreSQL are supported. Backend-specific size details are marked
unavailable when the SQLite `dbstat` facility or PostgreSQL catalog permission
is absent, while the remaining counts continue to render. The page follows the
existing Web authentication policy. This is an additive feature: it changes no
configuration, schema, API, or MCP contract and requires no migration.
