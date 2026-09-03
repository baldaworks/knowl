---
type: topic
title: Authentication & Session Security
knowl:
  id: concepts/authentication-and-session-security
  source_refs:
    - wiki-filesystem:engineering-docs/authentication-service.md@72f58ee1af237dec1f3c78631782b5127f1b1e8748e2195d5f19c86137e7971c
---
# Authentication & Session Security

Acme Cloud implements token-based authentication, distributed session revocation, and mutual TLS for inter-service communication.

## Authentication Protocols and Tokens
- **Client Authentication**: Authenticates via OAuth2 / OIDC providers or username and password hashed with bcrypt.
- **Access Tokens**: Short-lived JSON Web Tokens (JWT) with 15-minute expiration containing standard claims (`sub`, `exp`, `iss`, `jti`). Endpoints behind the API Gateway require valid bearer tokens.
- **Refresh Tokens**: Cryptographically random tokens with 7-day expiration.

## Session Revocation and Invalidation
- Revocation events are broadcasted across services using Redis Pub/Sub.
- Distributed bloom filter and blacklist keyed by `jti` track revoked sessions.
- Password changes and explicit logouts immediately invalidate active user refresh tokens in PostgreSQL.

## Inter-Service Security
- Internal service-to-service communication is secured using mutual TLS (mTLS) with SPIFFE identities.
