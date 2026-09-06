---
type: topic
title: Acme Cloud Platform
knowl:
  id: entities/acme-cloud-platform
  source_refs:
    - wiki-filesystem:engineering-docs/architecture-overview.md@85e388c68908fcde776b0728bf0534f78bd14b862abf85a6ac6fed3fdccbfdb2
---
# Acme Cloud Platform

The Acme Cloud Platform consists of modular services communicating over an event bus and REST/gRPC APIs.

## Services
- [[entities/api-gateway]]: Handles TLS termination, rate limiting, and route dispatching.
- [[entities/authentication-service]]: Manages user identity, JWT issuing, and token lifecycle.
- [[entities/inventory-service]]: Manages catalog and stock tracking backed by PostgreSQL.
- [[entities/order-processing-service]]: Consumes order submission events and drives state transitions.

## Storage Boundaries
- Primary operational state is stored in PostgreSQL clusters with read replicas.
- Short-lived caching and distributed locking use Redis clusters.
- File assets and audit archives reside in object storage with immutable bucket versioning.

## Guarantees & Resilience
All inter-service messages must provide at-least-once delivery with idempotent consumers.
