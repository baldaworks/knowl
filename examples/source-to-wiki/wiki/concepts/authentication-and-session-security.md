---
type: topic
title: Authentication & Session Security
knowl:
  id: concepts/authentication-and-session-security
  source_refs:
    - wiki-filesystem:engineering-docs/authentication-service.md@72f58ee1af237dec1f3c78631782b5127f1b1e8748e2195d5f19c86137e7971c
---
# Authentication & Session Security

## Authentication Protocol
- Clients authenticate using OAuth2/OIDC providers or username/password with bcrypt hashing.
- Successful authentication issues a short-lived JSON Web Token (JWT) access token (15-minute expiration) and a cryptographically random refresh token (7-day expiration).

## Session Revocation
- Access tokens contain standard claims including `sub`, `exp`, `iss`, and `jti`.
- Session revocations are broadcast over Redis Pub/Sub and recorded in a distributed bloom filter / blacklist keyed by `jti`.
- Password changes and explicit logouts immediately invalidate all active refresh tokens for the user in PostgreSQL.

## Security Requirements
- All endpoints behind the API Gateway require valid bearer tokens.
- Internal service-to-service communication uses mutual TLS (mTLS) with SPIFFE identities.
