# Authentication & Session Security

This document outlines the authentication protocols and token lifecycle.

## 1. Authentication Protocol
- Clients authenticate via OAuth2 / OIDC providers or username/password with bcrypt hashing.
- Successful authentication issues a short-lived JSON Web Token (JWT) access token (15 minutes expiry) and a cryptographically random refresh token (7 days expiry).

## 2. Session Revocation
- Access tokens contain standard claims including `sub`, `exp`, `iss`, and `jti`.
- Revoked sessions are broadcasted over Redis Pub/Sub and recorded in a distributed bloom filter / blacklist keyed by `jti`.
- Upon password change or explicit logout, all active refresh tokens for the user ID are invalidated in PostgreSQL immediately.

## 3. Security Requirements
- All endpoints behind the gateway require valid bearer tokens.
- Internal service-to-service communication uses mutual TLS (mTLS) with SPIFFE identities.
