---
type: topic
title: Acme Cloud Platform
knowl:
  id: entities/acme-cloud-platform
  source_refs:
    - wiki-filesystem:engineering-docs/architecture-overview.md@85e388c68908fcde776b0728bf0534f78bd14b862abf85a6ac6fed3fdccbfdb2
---
# Acme Cloud Platform

Acme Cloud is a modular cloud platform whose services communicate over an event bus and REST/gRPC APIs.

## Services
- **API Gateway**: Handles TLS termination, rate limiting, and route dispatching.
- **Authentication Service**: Manages user identity, JWT issuing, and token lifecycle.
- **Inventory Service**: Manages catalog and stock tracking backed by PostgreSQL.
- **Order Processing Service**: Consumes order submission events and drives state transitions.

## Storage Boundaries
- **PostgreSQL**: Stores primary operational state across clusters with read replicas.
- **Redis**: Provides short-lived caching and distributed locking.
- **Object Storage**: Stores file assets and audit archives with immutable bucket versioning.

## Resilience & Messaging Guarantees
- All inter-service messaging guarantees at-least-once delivery with idempotent consumers.
