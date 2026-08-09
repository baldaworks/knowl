# Workspace semantics

The workspace is the canonical Knowl artifact. It is portable Markdown plus
immutable source files; the SQL adapter is an operational index and projection.

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
- `wiki/**/*.md` is the accumulated knowledge artifact. Pages, `index.md`, and
  the append-only `log.md` are readable and Git-compatible.
- `.knowl/staging/` and `.knowl/recovery/` are implementation state used for
  preview, atomic commit, and interruption recovery. They are not knowledge
  content. The SQLite file is also rebuildable operational state.

`knowl init` creates the required directories and starter `schema.md`,
`wiki/index.md`, and `wiki/log.md` without replacing existing files.
`knowl validate` checks the required workspace paths. Full migration,
projection, and recovery checks happen during `start` or host construction.

## Page contract

The initial workspace groups pages under `entities`, `concepts`, and
`syntheses`, but the application accepts safe Markdown paths under `wiki/` and
the operator-owned schema defines the intended page types. A normal page uses
bounded YAML frontmatter followed by Markdown:

```markdown
---
id: entities/example
title: Example
type: entity
source_refs:
  - fixture:source-1@1
---
# Example

The page body is human-readable and cites the accepted source above.

See [[concepts/related]] or [[entities/other|display text]].
```

The enforced and linted conventions are:

- `id` is the safe page identifier and matches the path without `.md`.
- `title` and `type` identify the page for projections and maintenance.
- `source_refs` contains stable citations in the form
  `adapter:source-id@version`; every maintained page must cite at least one
  accepted raw source.
- Wiki links use `[[page/id]]`. A `|label` or `#fragment` may follow a link;
  graph extraction records the page target. Broken and malformed links are
  rejected before canonical mutation, and deterministic lint reports the same
  classes against the committed workspace.
- Edit plans may target `wiki/**/*.md` except `wiki/log.md`. The plan must
  carry the current schema digest, cite the accepted source, use unique paths,
  and stay within the configured file/count limits.

The starter `schema.md` is deliberately small. Operators should extend it with
page types, frontmatter fields, citation rules, link conventions, ingest and
query-filing policy, contradiction/supersession notation, and lint expectations.
Its byte digest is recorded with every operation and is rechecked before a
staged plan is committed.

## Control pages

`wiki/index.md` is the human-facing catalog. It remains a control document
without ordinary-page frontmatter, but its wiki links and simple `- page/id`
entries are still validated against the prospective canonical page set before
commit. Lint also reports missing or stale catalog entries and does not treat
the SQL projection as the catalog.

`wiki/log.md` is maintained by the application. Each committed operation adds
one structured JSON metadata line containing the operation ID, generation,
schema digest, cited source references, and committed file paths. A maintainer
cannot rewrite it as an arbitrary edit; its prior digest is part of staging and
commit preconditions.

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

The workspace is suitable for Git review, but Knowl never commits, pushes, or
synchronizes a remote repository. Operators commonly version `schema.md`,
`raw/`, and `wiki/`, while treating `.knowl/` as local operational state. If
`.knowl/` is versioned for a specific workflow, exclude credentials and review
the database/staging contents deliberately. Deletion or forgetting is not
implicit: absence from search is not an audited delete operation.
