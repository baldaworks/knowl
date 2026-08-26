# Workspace semantics

The workspace is the canonical Knowl artifact. `wiki/` is directly portable as
an Open Knowledge Format (OKF) v0.2 bundle, alongside immutable source files in
`raw/`; the SQL adapter is only an operational index and projection.

## Ownership and layout

```text
<workspace>/
├── schema.md
├── raw/
│   └── <scope/source identity>/<version>/
│       ├── source
│       └── manifest.yaml
├── wiki/
│   ├── index.md
│   ├── log.md
│   ├── entities/*.md
│   ├── concepts/*.md
│   └── syntheses/*.md
└── .knowl/
    ├── staging/
    ├── recovery/
    └── knowl.sqlite
```

The filesystem adapter safely tokenizes raw-source path components, so the
physical directory names need not equal the source ID. `manifest_ref` in an
accepted source identifies the stored manifest. The logical identity is always
the tuple `(scope, adapter, source ID, version)`.

Ownership is intentionally split:

- `schema.md` is operator-controlled policy. Maintainer plans may read it, but
  edit validation rejects schema changes.
- `raw/` contains accepted source bytes and a manifest. A source version is
  immutable and idempotent: replaying the same digest returns the existing
  record; the same identity/version with a different digest is a conflict.
- `wiki/**/*.md` is the accumulated semantic knowledge artifact. Pages,
  catalogs, and the append-only `log.md` are readable and Git-compatible. It
  contains no configured-source copies.
- `.knowl/staging/` and `.knowl/recovery/` are implementation state used for
  preview, atomic commit, and interruption recovery. They are not knowledge
  content. The SQLite file is also rebuildable operational state.

`knowl init` creates the required directories and starter `schema.md`,
`wiki/index.md`, and `wiki/log.md` without replacing existing files. The root
index declares `okf_version: "0.2"`.
`knowl validate` checks the required workspace paths. Full migration,
projection, and recovery checks happen during `start` or host construction.

## Page contract

The initial workspace groups pages under `entities`, `concepts`, and
`syntheses`, but the application accepts safe Markdown paths under `wiki/` and
the operator-owned schema defines the intended page types. A normal page is an
OKF concept with bounded YAML frontmatter followed by Markdown:

```markdown
---
title: Example
type: entity
description: A portable example.
tags: [example]
knowl:
  id: entities/example
  source_refs:
    - fixture:source-1@1
---
# Example

The page body is human-readable and cites the accepted source above.

See [[concepts/related]] or [[entities/other|display text]].
```

The enforced and linted conventions are:

- the safe bundle-relative path without `.md` is the concept identity;
- `type` is required; `title`, `description`, `resource`, `tags`, `sources`,
  lifecycle, generation, and verification fields follow OKF v0.2;
- unknown producer fields round-trip as OKF extensions;
- the `knowl` namespaced extension carries Knowl provenance. Its `source_refs`
  contains stable citations in the form
  `adapter:source-id@version`; every maintained page must cite at least one
  accepted raw source.
- Curated Knowl pages use `[[page/id]]`; these links remain strict. Imported OKF
  concepts use standard Markdown links such as `[Related](related.md)`. Local
  concept targets are resolved deterministically; external URLs, assets, and
  broken targets are tolerated and excluded from the concept graph.
- Maintainer edit plans may target safe semantic `wiki/**/*.md` except
  `wiki/log.md` and the reserved legacy `wiki/sources/**` boundary. The plan must carry the
  current schema digest, cite the accepted source, use unique paths, and stay
  within the configured file/count limits.

Accepted raw manifests carry structured `source_document` metadata containing
`source_id`, `document_id`, immutable `revision`, and canonical `uri`. Curated
pages cite stable raw refs; snapshots resolve all supporting refs into a sorted
`source_documents` collection without copying source bodies into the page.
A successful complete scan may tombstone a deleted document, while its raw
revisions and curated semantic pages remain. An interrupted scan never
authorizes deletion.

The starter `schema.md` is deliberately small. Operators should extend it with
page types, frontmatter fields, citation rules, link conventions, ingest and
query-filing policy, contradiction/supersession notation, and lint expectations.
Its byte digest is recorded with every operation and is rechecked before a
staged plan is committed.

## Control pages

`wiki/index.md` is the OKF root catalog and declares version `0.2`. Nested
`index.md` files are also reserved catalogs but cannot redeclare the bundle
version. Catalog controls are validated and included in canonical digests, but
never become retrieval evidence.

`wiki/log.md` is maintained by the application. Each committed operation adds
one structured JSON metadata line containing the operation ID, generation,
schema digest, cited source references, and committed file paths. A maintainer
cannot rewrite it as an arbitrary edit; its prior digest is part of staging and
commit preconditions.

Every `log.md` is a reserved, newest-first ISO-date-grouped OKF control. Source
OKF logs remain immutable raw evidence and are not imported into the semantic
wiki. Attested Computation metadata on semantic pages is parsed, preserved,
projected, and returned, but never executed or dereferenced.

## Explicit OKF migration

Startup and read-only commands never migrate content. To upgrade a legacy
canonical workspace, stop writers, take a backup, and run:

```bash
knowl migrate okf-v0.2
knowl validate
```

The command preflights the whole change, uses the normal recovery journal,
archives the exact legacy log, writes its marker only after canonical commit,
and rebuilds the configured projection. Re-running it is a no-op. If interrupted,
the next invocation deterministically rolls back or completes from the journal.

## Recovery and Git

Content commit writes a staging manifest, records preimages in a recovery
journal, atomically replaces the target files, and marks the journal committed.
Startup recovery runs before projection readiness:

- a `prepared` journal restores preimages and records a rolled-back operation;
- a `committed` journal is cleaned after the canonical files are complete;
- incomplete staging is discarded as uncommitted work.

The host repeats recovery during shutdown so interrupted operations remain
inspectable through redacted operation status. Do not hand-edit staging or
recovery files while a host is running. For a backup, stop the host first and
copy the complete workspace; the SQL projection can be rebuilt from its
Markdown snapshot.

Source synchronization uses the same staged canonical writer and recovery
journal. On restart, filesystem recovery completes before resumable source runs
and projection preparation. One source failure does not remove another source's
raw evidence or make the prior successful semantic snapshot unavailable. Sync
success covers raw acceptance and durable maintenance reservation; the required
maintainer provider runs asynchronously through the operation scheduler. Bounded
maintenance outcomes are reported separately in source status.

Legacy `wiki/sources/<source_id>/**` files are derived migration input, not
active knowledge. The next successful reconciliation deletes only the matching
source subtree through staged recovery and leaves raw history and curated pages
unchanged.

The workspace is suitable for Git review, but Knowl never commits, pushes, or
synchronizes a remote repository. Operators commonly version `schema.md`,
`raw/`, and `wiki/`, while treating `.knowl/` as local operational state. If
`.knowl/` is versioned for a specific workflow, exclude credentials and review
the database/staging contents deliberately. Deletion or forgetting is not
implicit: absence from search is not an audited delete operation.
