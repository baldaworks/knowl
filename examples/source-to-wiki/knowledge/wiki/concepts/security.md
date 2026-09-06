---
type: topic
title: Security
knowl:
  id: concepts/security
  source_refs:
    - wiki-filesystem:engineering-docs/authentication-service.md@72f58ee1af237dec1f3c78631782b5127f1b1e8748e2195d5f19c86137e7971c
---
# Security

Cross-cutting security policies and requirements across the [[entities/acme-cloud-platform]].

## Endpoint Security
- All endpoints behind the [[entities/api-gateway]] require valid bearer tokens issued by the [[entities/authentication-service]].

## Service Communication
- Internal service-to-service communication uses mutual TLS (mTLS) with SPIFFE identities.
