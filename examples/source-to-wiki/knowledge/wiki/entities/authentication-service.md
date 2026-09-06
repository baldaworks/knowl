---
type: topic
title: Authentication Service
knowl:
  id: entities/authentication-service
  source_refs:
    - wiki-filesystem:engineering-docs/architecture-overview.md@85e388c68908fcde776b0728bf0534f78bd14b862abf85a6ac6fed3fdccbfdb2
    - wiki-filesystem:engineering-docs/authentication-service.md@72f58ee1af237dec1f3c78631782b5127f1b1e8748e2195d5f19c86137e7971c
---
# Authentication Service

The Authentication Service is a core service in the [[entities/acme-cloud-platform]]. It manages user identity, JWT issuing, and token lifecycle.

## Authentication Protocols
- Clients authenticate via OAuth2 / OIDC providers or username/password with bcrypt hashing.
- Successful authentication issues a short-lived JSON Web Token (JWT) access token with a 15-minute expiry and a cryptographically random refresh token with a 7-day expiry.

## Token Lifecycle and Session Revocation
- Access tokens contain standard claims including `sub`, `exp`, `iss`, and `jti`.
- Revoked sessions are broadcasted over Redis Pub/Sub and recorded in a distributed bloom filter / blacklist keyed by `jti`.
- Upon password change or explicit logout, all active refresh tokens for the user ID are invalidated in PostgreSQL immediately.
