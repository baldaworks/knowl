---
id: entities/architecture-overview
title: Architecture Overview
type: entity
version: "0.2"
parent: catalogs/engineering
source_refs:
  - wiki-filesystem:engineering-docs/architecture-overview.md
---
# Architecture Overview

## Overview
The platform architecture consists of four core microservices communicating over a message event bus and TLS-terminated REST/gRPC endpoints.

## Core Components
- **API Gateway**: Provides external ingress, routing, rate limiting, and TLS termination.
- **Authentication Service**: Owns identity management, cryptographic JWT issuance, and session token invalidation.
- **Inventory Service**: Owns transactional stock management backed by PostgreSQL clusters.
- **Order Processing Service**: Consumes order events asynchronously with at-least-once idempotent processing.

## Storage Architecture
- **PostgreSQL**: Operational system of record with hot standbys.
- **Redis**: Low-latency caching and distributed locks.
- **Object Storage**: Immutable versioned storage for document assets and cold audit records.
