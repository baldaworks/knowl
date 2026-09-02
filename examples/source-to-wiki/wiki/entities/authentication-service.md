---
id: entities/authentication-service
title: Authentication Service
type: entity
version: "0.2"
parent: catalogs/engineering
source_refs:
  - wiki-filesystem:engineering-docs/authentication-service.md
---
# Authentication Service

## Overview
Identity service handling OAuth2/OIDC flows, password verification using bcrypt, and token lifecycle management.

## Token Specifications
- **Access Tokens**: Short-lived JSON Web Tokens (JWT) with a 15-minute expiration period. Standard claims include `sub`, `exp`, `iss`, and `jti`.
- **Refresh Tokens**: Cryptographically secure random tokens valid for 7 days, stored hashed in PostgreSQL.

## Distributed Session Revocation
- Token revocation is broadcast over Redis Pub/Sub channels to all API gateway instances.
- Gateways maintain an in-memory bloom filter / blacklist keyed by `jti` for instantaneous request rejection without database lookups.
- User password changes immediately invalidate all active refresh tokens in PostgreSQL.
