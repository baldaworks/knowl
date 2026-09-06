---
type: topic
title: API Gateway
knowl:
  id: entities/api-gateway
  source_refs:
    - wiki-filesystem:engineering-docs/architecture-overview.md@85e388c68908fcde776b0728bf0534f78bd14b862abf85a6ac6fed3fdccbfdb2
    - wiki-filesystem:engineering-docs/authentication-service.md@72f58ee1af237dec1f3c78631782b5127f1b1e8748e2195d5f19c86137e7971c
---
# API Gateway

The API Gateway is a core service in the [[entities/acme-cloud-platform]]. It handles TLS termination, rate limiting, and route dispatching. All endpoints behind the gateway require valid bearer tokens.
