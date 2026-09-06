# Core Architecture & System Topology

This document describes the high-level architecture of the Acme Cloud platform.

## 1. System Overview
The platform consists of modular services communicating over an event bus and REST/gRPC APIs:
- **API Gateway**: Handles TLS termination, rate limiting, and route dispatching.
- **Authentication Service**: Manages user identity, JWT issuing, and token lifecycle.
- **Inventory Service**: Manages catalog and stock tracking backed by PostgreSQL.
- **Order Processing Service**: Consumes order submission events and drives state transitions.

## 2. Storage Boundaries
- Primary operational state is stored in PostgreSQL clusters with read replicas.
- Short-lived caching and distributed locking use Redis clusters.
- File assets and audit archives reside in object storage with immutable bucket versioning.

## 3. Guarantees & Resilience
All inter-service messages must provide at-least-once delivery with idempotent consumers.
