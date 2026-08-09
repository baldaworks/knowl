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
edit plan. The independent `provider.Config` describes model ID, model name,
endpoint, credential reference, reasoning settings, timeout, and input/output
limits. `Config.Validate` rejects incomplete or unsafe settings, and
`Config.Redacted` is suitable for diagnostics without exposing credentials.

The provider receives a bounded `MaintenanceInput` containing schema, accepted
source metadata/text, selected pages, and read limits. It returns data-only
`ModelEditPlan` output. The application validates schema digest, source
citations, allowed paths, file count/size, and rationale limits before staging.
Provider code never receives unrestricted filesystem authority and never edits
schema, raw sources, or log files.

The current `knowl start` command does not select a remote provider by default.
Embedding code or a later provider configuration layer must make that choice
explicit; this prevents credentials and reasoning policy from being inherited
implicitly from Balda.

## Out of scope

The current contract intentionally excludes automatic web research or URL
fetching, remote/shared tenancy, vector databases, encryption, binary/image
understanding, automatic Git push/sync, mutating MCP tools, implicit forgetting,
and deletion of immutable raw sources. Those capabilities require explicit
future contracts and ownership decisions.
