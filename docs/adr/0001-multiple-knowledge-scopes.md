# ADR 0001: Support multiple isolated knowledge scopes

- Status: Accepted
- Date: 2026-11-21
- Approved: 2026-11-21
- Owners: Knowl maintainers
- Decision scope: public scope selection, canonical OKF storage, processing,
  authorization, recovery, and compatibility
- Supersedes: none

## Context

Knowl currently runs one canonical workspace and binds each host to one trusted
`ScopeRef`. Public callers cannot override that scope. The default host scope is
`local`, and the canonical filesystem layout has one `schema.md`, `raw/`, and
`wiki/` tree. A host can also be configured with a different trusted scope
reference.

The domain and operational stores are already largely scope-aware. Operations,
pages, links, projection state, source state, page provenance, and embedding
chunks include `scope` in their identities or indexes. Application ports pass
an opaque `ScopeRef`, and `ScopeAuthorizer` defines a policy boundary. However,
the filesystem adapter resolves every scope into the same canonical paths, the
runtime composes one configured scope, the scheduler scans one scope, and one
publication coordinator serializes the entire host. The existing SQL shape
therefore does not by itself provide multiple isolated knowledge bases.

Operators need one Knowl host to maintain independent bodies of knowledge. A
caller that omits the selection must continue to use a predictable default.
Each selected body must remain a directly portable OKF v0.2 bundle, and no
ingest, maintenance, hierarchy, retrieval, recovery, or export operation may
cross its boundary implicitly.

Introducing a second product term such as `realm` would overlap the existing
meaning of scope and create an unnecessary `realm -> scope -> storage` mapping.
Semantic domains, catalogs, and tags are organization inside a knowledge base;
they are not isolation boundaries.

## Decision

Knowl will promote **scope** to the public name of an isolated knowledge base.
It will not introduce `realm`.

One scope owns exactly one canonical policy document, immutable raw history,
and directly portable OKF bundle. The host may contain multiple scopes, while
operational SQL and worker capacity remain shared.

### Scope identity

The public and durable identities have different responsibilities:

```go
// ScopeName is the validated, caller-visible name of a configured scope.
type ScopeName string

// ScopeRef is the opaque, durable identity used by domain and storage ports.
type ScopeRef string
```

`ScopeName` uses a conservative lowercase slug contract. The initial contract
accepts 1 to 63 ASCII characters matching:

```text
[a-z0-9][a-z0-9-]{0,62}
```

Names are immutable in the first release. Scope creation, deletion, and rename
are configuration and migration concerns, not public business operations.

Trusted resolution maps a configured `ScopeName` to a `ScopeRef` and a canonical
workspace. Public transport input is never cast directly to `ScopeRef`. For a
new installation, the public `default` scope resolves to the durable
`ScopeRef("local")`. An existing installation retains its configured
`ScopeRef` for `default`, including a non-`local` value. New scopes receive
durable references from a versioned, collision-free internal namespace, such
as `scope:<name>`. Configuration must reject collisions with existing
references, including the retained legacy reference. The mapping is stable for
the lifetime of the scope and is not inferred from arbitrary filesystem paths.

An absent or blank public selection resolves to the configured default scope.
The initial default name is `default`. Unknown, malformed, disabled, or
unauthorized names fail before any canonical or operational read for the
selected scope.

### Canonical storage and OKF boundary

Existing installations keep the `default` scope at the legacy workspace root.
Named scopes live under `scopes/<name>/`:

```text
<workspace>/
├── schema.md                 # legacy default
├── raw/
├── wiki/
│   ├── index.md
│   ├── log.md
│   └── ...
├── scopes/
│   └── infrastructure/
│       ├── schema.md
│       ├── raw/
│       └── wiki/
└── .knowl/
    ├── staging/<internal-scope-key>/
    └── recovery/<internal-scope-key>/
```

A new installation may instead place `default` at `scopes/default/`, alongside
other named scopes. The operational store may use SQLite or PostgreSQL; its
location remains configured independently of the canonical scope roots.

A trusted `ScopeWorkspaceResolver` encapsulates both default layouts and
resolves each configured scope to its canonical workspace. It must persist the
layout choice or resolve it deterministically across restarts. If both the
root default and `scopes/default/` contain possible canonical default data, or
the layout otherwise cannot be resolved unambiguously, it fails closed and
requires operator resolution; it must not choose one silently, merge them, or
initialize over either.

All canonical operations use the resolver and its trusted workspace result
rather than constructing or assuming paths. This includes initialization,
ingest, maintenance, hierarchy, retrieval, validation, projection, export,
backup-related tooling, staging, commit, and recovery. The legacy workspace
root is a container for other scopes and host state as well as the default
scope: default-scope access is limited to its resolved canonical paths and
must never treat `scopes/` or `.knowl/` as part of its bundle. Resolved
canonical paths for different scopes must be disjoint, with containment and
symlink checks enforcing that boundary.

Each resolved `wiki/` directory is an independent OKF v0.2 bundle with its own
root `index.md`, append-only `log.md`, catalogs, pages, links, snapshot digest,
and validation result. Exporting one scope copies that `wiki/` bundle without
adding server tenancy metadata to every page. An exported bundle does not
depend on its original scope name.

Each scope also owns its own `schema.md` and `raw/` history. Source identity
remains `(scope, adapter, source_id, version)`, and an accepted revision is
written only below the resolved scope's canonical raw path. No source or page
is shared by filesystem reference between scopes.

`.knowl/` contains host-level operational state. Staging, recovery, and commit
receipts must be partitioned by a safely encoded internal scope key, not by
unchecked caller input. Containment checks reject traversal, unsafe symlinks,
and any plan or recovery record that resolves outside the selected scope's
permitted canonical paths.

Relative OKF links resolve only within the current bundle. Cross-scope links,
shared pages, inherited schemas, and implicit catalog composition are not
supported by this decision.

### Configuration

Scopes and their sources are configured explicitly. The following illustrates
ownership; it is not a complete configuration example:

```yaml
knowl:
  default_scope: default
  scopes:
    default:
      sources:
        - balda-docs
    infrastructure:
      sources:
        - deployment-docs
```

The exact source configuration retains the existing tagged source shape. A
configured source belongs to exactly one scope. Equal adapter and source IDs
in different scopes are independent identities.

The first release requires at least one configured scope and requires
`default_scope` to name one of them. A legacy configuration with one workspace
and no `scopes` block normalizes to its existing durable scope and legacy root.
It continues to work without moving canonical data. Adding another configured
scope does not change that default workspace.

### Public surfaces

The three existing business operations gain one optional `scope` selector:

- retrieve accepts a query and optional scope;
- ingest accepts one source and optional scope;
- operation status accepts an operation ID and optional scope.

CLI workflows that read, mutate, validate, rebuild, synchronize, reconcile, or
export canonical state accept `--scope`; omission selects `default_scope`.
Operator UI navigation remains within one selected and authorized scope.

Responses include the resolved public scope name where it is useful for audit
and diagnostics. They do not expose the internal `ScopeRef` or physical path.
Operation status is looked up through the selected scope even when operation
IDs are globally collision-resistant, preventing an ID from becoming a
cross-scope read capability.

This remains the same three-operation product contract. Scope listing and
administration are not added to MCP ingest/retrieve/status tools by this ADR.

### Authorization and trusted resolution

Scope selection changes the current rule from “callers cannot override the
host scope” to “callers may select an authorized configured scope.” The
trusted sequence is:

```text
transport input
  -> normalize ScopeName or choose default
  -> resolve configured ScopeContext
  -> authorize principal, scope, and operation kind
  -> pass trusted ScopeRef and workspace to application services
```

The resulting context is immutable for the operation:

```go
type ScopeContext struct {
    Name      ScopeName
    Ref       ScopeRef
    Workspace ScopeWorkspace
}
```

Authentication and authorization remain separate. A deployment may initially
grant one authenticated operator access to every configured scope, but the
application boundary always invokes `ScopeAuthorizer` so a later policy can
grant a principal only a subset. Public and read-only facade modes do not imply
permission to enumerate or select every scope. The trusted principal and
authorization policy for each transport, including local CLI use, must be
defined as part of implementation.

### Processing

Scope is resolved once at ingress and carried as trusted context through the
complete processing lifecycle:

```text
resolve and authorize scope
  -> accept source into the resolved raw path
  -> reserve durable operation under ScopeRef
  -> build maintenance context from that scope
  -> validate a scope-relative edit plan
  -> stage under that scope
  -> acquire that scope's publication gate
  -> commit the resolved wiki and its log
  -> snapshot that complete scope
  -> publish lexical and vector projections under ScopeRef
  -> persist the terminal operation outcome
```

The maintainer receives only the selected scope's schema, accepted source,
pages, provenance, and snapshot. Plan validation rejects edits outside that
scope before staging. Hierarchy reconciliation has its own root, catalogs,
snapshot, and log per scope and cannot inspect adjacent scopes.

Projection remains a full-snapshot publication for one scope. Existing SQL
keys such as `(scope, page_id)` and `(scope, space, page_id, ordinal)` support
the isolation boundary for lexical and vector state. Projection readiness,
embedding coverage, and evidence verification are evaluated against the same
scope snapshot. A result selected from SQL is verified against that scope's
canonical bundle before it becomes evidence.

There is no implicit multi-scope retrieval. A future federated query would be
a separate explicit contract with per-scope authorization, bounded fan-out,
ranking semantics, and citations that retain scope identity. This ADR does not
define `scope=*` or an array of scopes.

### Scheduling and publication

Maintainer runtime capacity remains host-level. For example, `workers: 2`
permits at most two owned maintainer executions across all scopes, not two per
scope.

Durable scheduling expands from one configured scope to all configured scope
references. Ready work is admitted fairly across scopes so a hot scope cannot
indefinitely starve another. Wake hints remain lossy; durable scans recover
missed work for every configured scope.

Canonical publication is serialized per scope rather than per host. Commits in
the same scope retain their current ordering and conflict behavior. Commits in
different scopes may proceed concurrently because they mutate disjoint bundles
and projection partitions. The coordinator is therefore keyed by trusted
`ScopeRef`, with bounded lifecycle cleanup for removed configuration entries.

Within one scope, the publication gate continues to cover canonical commit,
complete projection publication, and durable terminal outcome. Inference runs
outside the gate. No scope may report a committed operation while its matching
projection tail is incomplete.

### Recovery, validation, and readiness

Startup discovers only configured scopes, resolves and validates their
workspaces, and performs recovery independently for each. Recovery journals
and commit receipts carry and verify the internal scope identity. Replay uses
the resolver and cannot redirect to another scope or to host-level paths. The
legacy default may replay only to its permitted root canonical paths, not to
arbitrary paths under the host root.

Validation and projection rebuild operate on one explicit scope or on a
deterministic snapshot of all configured scopes. Host-wide commands report a
result for every scope rather than stopping after the first successful scope.

Readiness distinguishes host dependencies from scope state. A shared database
failure can make the host unavailable. A canonical recovery or validation
failure confined to one scope makes that scope unavailable without discarding
healthy published state in other scopes. Health and operator diagnostics expose
bounded per-scope state without leaking page content or physical paths.

### Compatibility and optional normalization

Knowl does not silently reinterpret or move an existing canonical workspace.
Upgrading or enabling a second scope must not require moving legacy canonical
data. The existing root `schema.md`, `raw/`, and `wiki/` remain the default
scope, and its configured durable `ScopeRef`, immutable raw manifests,
operation identities, provenance, and append-only audit history are preserved.
Accepted evidence is not rewritten merely to give the scope the public name
`default`.

An optional normalization command may move the legacy default to
`scopes/default/`. If provided, it must preflight recovery state, ambiguity,
and path containment; require exclusive writer ownership; preserve the same
durable identity and canonical history; and update path-bearing operational
state that cannot safely be rebuilt. It must rebuild projections as needed.
The individual moves need not be one filesystem transaction: a durable journal
must make interruption recoverable and prevent normal writers from observing or
using a partially normalized layout. The layout choice is committed only after
the canonical move is complete. Normalization is never a prerequisite for
upgrade or multi-scope use.

A downgrade to software that understands only the legacy root cannot represent
additional scopes. After multi-scope writes, an operator must preserve or export
those scopes and follow a separately defined downgrade procedure; no automatic
downgrade is promised.

## Consequences

### Positive

- Knowl reuses its existing scope-aware domain and SQL contracts.
- Each scope remains independently inspectable and exportable as ordinary OKF.
- Canonical, projection, authorization, and recovery boundaries align.
- The default preserves simple clients that do not need scope selection.
- Existing installations can add scopes without moving legacy canonical data.
- Independent scopes can publish concurrently while sharing bounded provider
  capacity.
- The design leaves room for finer authorization without making `realm` a
  competing product concept.

### Costs and risks

- Filesystem composition, scheduling, recovery, CLI, HTTP, MCP, Web UI, and
  diagnostics must all become explicitly scope-aware.
- The resolver must safely support both default layouts and reject ambiguous
  or overlapping canonical paths.
- Per-scope publication coordination and fair scheduling add host-level state.
- Partial scope readiness is more complex than the current all-or-nothing host
  model.
- Operators must understand that a semantic domain or catalog is not an
  isolation boundary.

## Rejected alternatives

### Introduce `realm` above the existing scope

No independent lifecycle or policy currently requires two isolation levels.
Adding `realm -> scope` would duplicate identity, authorization, storage, and
API vocabulary. If a future tenant must contain several knowledge scopes, that
requirement needs a separate ADR with concrete tenant ownership and policy.

### Use one OKF bundle and store scope on every page

One root index, log, hierarchy, link graph, and export would remain shared.
Every canonical operation would require filtering a common bundle, and a
missed filter could leak data. A bundle per scope makes containment structural.

### Require a uniform `scopes/default/` layout before enabling multiple scopes

A uniform layout would simplify path conventions, but requiring existing
installations to move canonical data makes upgrade and second-scope adoption
depend on a filesystem migration. Trusted workspace resolution supports both
layouts while retaining explicit containment and recovery guarantees.

### Expose `ScopeRef` directly to callers

`ScopeRef` is durable trusted identity, not user input. Direct exposure would
couple clients to compatibility aliases such as `local` and allow unchecked
storage keys to cross the authorization boundary. Callers select a validated
configured `ScopeName` instead.

### Search every scope when scope is omitted

Omission means the configured default, preserving bounded and predictable
behavior. Implicit fan-out would make authorization, ranking, citations,
latency, and failure semantics ambiguous.

### Run one complete host and database per scope

Separate deployments remain valid when operators need infrastructure-level
isolation. Requiring them for every logical knowledge boundary duplicates
listeners, providers, worker capacity, configuration, and operational stores
and does not use the existing scope-aware application model.

## Delivery plan

1. Add `ScopeName`, trusted resolution, normalized multi-scope configuration,
   and compatibility mapping from `default` to the existing durable scope
   without changing the single-scope runtime behavior.
2. Introduce `ScopeWorkspaceResolver` and use resolved canonical paths for
   filesystem adapters, staging, recovery, validation, and export. Preserve
   legacy-root default workspaces and reject ambiguous or overlapping layouts.
3. Compose per-scope source ownership, maintenance context, hierarchy, and
   projection while retaining shared stores and provider capacity.
4. Replace the single-scope scheduler and publication gate with fair
   multi-scope scheduling and per-scope coordination.
5. Add authorized optional scope selection to CLI, HTTP, MCP, and operator UI,
   keeping omission equivalent to the configured default.
6. Add compatibility, layout ambiguity, isolation, recovery, concurrency,
   authorization, and cross-scope negative tests for SQLite and PostgreSQL.
   If normalization is implemented, test interruption and recovery as well.
7. Document deployment, backup, export, readiness, and downgrade procedures.
