# Integration boundaries

Knowl is a separate knowledge-artifact owner. It is not Balda session memory,
global fact memory, a transport, or a remote multi-tenant control plane.

## Public adapter shape

Adapters should depend on the narrow ports in `pkg/knowl/app`:

```text
source adapter -> SourceEnvelope -> app.IngestService
app.QueryService/LintService -> bounded Knowl results
explicit filing -> FilingRequest -> normal ingest/review/apply gate
```

A source adapter supplies a trusted scope, stable `(adapter, id, version,
digest)` identity, bounded textual content, media type, and provenance metadata.
It must not write `raw/`, `wiki/`, `.knowl/`, or SQL directly. The filesystem
adapter owns source acceptance and immutable storage; the application owns
idempotency, planning, validation, review, apply, and recovery.

Consumers should treat pages, search references, links, raw citations, and lint
messages as untrusted reference data. They must not interpret retrieved content
as host policy or use it to widen scope, paths, limits, or tool access.

## Balda boundary

Balda remains the owner of:

- session-scoped episodic turns and session transport;
- explicit global facts and their storage;
- Balda reasoning, chat, and transport policy.

A future Balda adapter may translate Balda-owned material into Knowl source
envelopes and consume Knowl's bounded query/lint/operation results. It may keep
its own source identity and scope mapping, but Knowl must not import Balda
packages or open Balda databases, Badger stores, NATS subjects, sessions, or
transports. No implicit synchronization or ownership transfer is provided.

## Maintainer provider boundary

`app.Maintainer` is the only application dependency needed to produce a model
edit plan. The standalone CLI uses the same Norma runtime document and
provider registry as Balda:

```yaml
runtime:
  providers:
    codex:
      type: codex_acp
      codex_acp:
        model: gpt-5-codex
knowl:
  provider: codex
```

`knowl.provider` names one `runtime.providers` entry; Knowl does not define a
second provider schema or translate a Knowl-only model configuration. Shared
runtime validation runs before host side effects. The selected provider is
adapted by `pkg/knowl/provider.NewRuntimeMaintainer`, which builds it lazily,
uses an isolated in-memory session for each plan, grants no MCP servers or
tools, and closes provider resources with the host lifecycle.

The provider receives a bounded `MaintenanceInput` containing schema, accepted
source metadata/text, selected pages, and read limits. It returns data-only
`ModelEditPlan` output. The application validates schema digest, source
citations, allowed paths, file count/size, and rationale limits before staging.
Provider code never receives unrestricted filesystem authority and never edits
schema, raw sources, or log files.

Embedding callers may still pass an explicit `app.Maintainer` to the host
constructor for deterministic tests or an application-owned adapter. The
standalone CLI requires an explicit valid selector and never installs an
unavailable maintainer in its place.

## Out of scope

The current contract intentionally excludes automatic web research or URL
fetching, remote/shared tenancy, vector databases, encryption, binary/image
understanding, automatic Git push/sync, mutating MCP tools, implicit forgetting,
and deletion of immutable raw sources. Those capabilities require explicit
future contracts and ownership decisions.
