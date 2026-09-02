# Single-Source Knowledge Showcase Example

This example demonstrates how to use **Knowl** as a centralized, queryable knowledge base for an engineering project using the **one-shot run workflow** (`knowl run` / `Host.RunOnce`).

Rather than running a persistent daemon or sending ad-hoc ingest payloads over HTTP, this showcase points Knowl at a single authoritative folder of engineering documents, runs a complete synchronization and synthesis cycle, and queries the resulting Open Knowledge Format (OKF) Markdown wiki for grounded answers with citations.

---

## The Knowledge Corpus

The source directory (`sources/`) contains realistic project documentation representing typical engineering artifacts:

1. **`architecture-overview.md`**: High-level platform topology, services (API Gateway, Auth, Inventory, Orders), and storage boundaries (PostgreSQL, Redis, Object Storage).
2. **`authentication-service.md`**: Authentication flows, JWT access token expiry (15 minutes), refresh tokens, and distributed session revocation using Redis Pub/Sub and PostgreSQL invalidation.
3. **`database-retention-policy.md`**: Data lifecycle rules, 2-year active retention for transactional records, cold-archiving to parquet, audit log preservation, and GDPR cascade purge workflows.
4. **`incident-response-runbook.md`**: Triage guidelines (SEV-1 vs SEV-2), Patroni PostgreSQL failover sequence, replica promotion, and incident communication channels.

---

## How It Works

1. **Source Configuration**:
   Knowl registers the document folder as an authoritative `filesystem` source:
   ```go
   config.Sources = []domain.Source{
       {
           ID:      "engineering-docs",
           Type:    domain.SourceTypeFilesystem,
           Enabled: true,
           Config: domain.SourceConfig{
               Filesystem: &domain.FilesystemSourceConfig{
                   Root:    sourcesDir,
                   Include: []string{"**/*.md"},
                   Flavor:  domain.SourceFlavorMarkdown,
               },
           },
       },
   }
   ```

2. **One-Shot Pipeline Execution (`RunOnce`)**:
   In a single in-process call without binding network sockets:
   - Synchronizes raw source files into immutable versions under `raw/`.
   - Queues durable maintenance operations.
   - Claims and drains the operation queue to completion.
   - Saves normalized semantic wiki pages (`wiki/entities/*.md`) and root catalog links (`wiki/index.md`).

3. **Knowledge Retrieval & Provenance**:
   Developers or autonomous agents query the knowledge base via `host.Query().Search()`:
   - Queries match against lexical indexes and semantic OKF concepts.
   - Results return untrusted snippets together with exact **`SourceDocuments`** provenance (`engineering-docs/<filename>@<revision>`), guaranteeing traceability back to the source documents.

---

## Running the Showcase

### Run the deterministic test
The test validates the end-to-end knowledge loop using an in-process maintainer mock (no external LLM API key or network required):

```bash
go test -v ./examples/single-source-showcase
```

### Run with a configured Knowl LLM maintainer
To run the executable program against live configured providers (e.g. OpenAI, Anthropic, Gemini, or OpenCode via `.config/knowl/config.yaml`):

```bash
go run ./examples/single-source-showcase
```
