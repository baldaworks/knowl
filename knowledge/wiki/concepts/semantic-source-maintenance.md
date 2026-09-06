---
type: topic
title: Semantic Source Maintenance
knowl:
  id: concepts/semantic-source-maintenance
  source_refs:
    - wiki-filesystem:knowl-docs/releases/v0.3.0.md@68b799fdc477f93914692c3f66603905eee292e9bb64a1362063b090f319bee1
    - wiki-filesystem:knowl-docs/releases/v0.3.1.md@b1edbf1e4b747269157429171f860cbf7f1dce300304dedc7e75b6f528c0dd30
    - wiki-filesystem:knowl-docs/releases/v0.2.0.md@4d7a07f5afbdfc49ae07e300c851d7f8ab6a03bbb25b93c42203f8102c18bfe0
---
# Semantic Source Maintenance

Semantic Source Maintenance governs how Knowl ingests external source revisions as immutable raw evidence and asynchronously synthesizes them into a curated, maintainer-owned semantic wiki.

## Core Principles

- **Separation of Evidence and Knowledge**: Source revisions are accepted into `raw/` as immutable evidence rather than mirrored directly as active wiki pages inside the portable Open Knowledge Format (OKF) bundle.
- **Maintainer Provider Requirement**: A runnable Knowl Host must have an explicitly injected maintainer or a valid `knowl.provider` selected from `runtime.providers` before achieving readiness (`/readyz`). When operating provider-free, source synchronization and retrieval remain fully operational, while ingest returns the stable `maintainer_unavailable` outcome.
- **Asynchronous Maintenance**: Successful source synchronization guarantees raw acceptance and durable maintenance reservation. Model-backed updates to wiki content execute asynchronously and report progress via source status.

## Semantic Wiki Architecture

- **Curated Semantic Entities and Concepts**: Maintainers construct and update root-reachable semantic OKF entities, concepts, syntheses, and catalogs rather than mirroring raw source file paths.
- **Provenance and Lineage**: Curated factual pages cite accepted raw references (`knowl.source_refs`). Page updates preserve unrelated source citations and only replace older revisions from the same source document lineage.
- **Multi-Source Synchronization and Identity**: Sources use `(source_id, document_id, revision)` tuple identity, preventing path collisions across sources with identical relative paths. Unchanged documents are not re-fetched or rewritten.
- **Cross-Source Evidence Aggregation**: Multiple distinct configured sources can support a single shared semantic page. Retrieval returns a sorted collection of contributing `source_documents`, and filtering by any contributing source matches the shared page.
- **Control Page Isolation**: Catalogs and control pages are excluded from retrieval evidence. Both SQLite and PostgreSQL operational stores use the same rebuildable page-to-source projection.

## Lifecycle, Migration, and Provenance Backfill

- **Legacy Provenance Backfill**: Reconciliation enriches legacy raw manifests with validated `source_document` identity while reusing already accepted immutable source bytes. Unchanged documents are not re-fetched, and the enrichment is conflict-safe and one-way (existing provenance cannot be overwritten by a different document identity).
- **Projection Rebuild**: When source sync backfills legacy provenance, it rebuilds the projection to record `source_documents` and page-to-source relations for citing semantic pages, ensuring source-filtered retrieval (`knowl retrieve --source <id>`) returns the same relevant evidence as unfiltered retrieval.
- **Staged Removal of Legacy Mirrors**: On reconciliation, Knowl safely deletes legacy derived `wiki/sources/<source_id>/**` mirrors using staged recovery while preserving raw revisions and curated semantic pages.
- **Scan-Gated Tombstoning**: Upstream deletions tombstone active source state only after a complete scan; interrupted scans never delete source documents or accumulated wiki knowledge.
- **Observability**: Source status provides deterministic, bounded counts and samples for queued, retrying, replayed, committed, and failed maintenance operations.
