---
id: entities/incident-response-runbook
title: Incident Response Runbook
type: entity
version: "0.2"
parent: catalogs/engineering
source_refs:
  - wiki-filesystem:engineering-docs/incident-response-runbook.md
---
# Incident Response Runbook

## Severity Classifications
- **SEV-1**: Critical outage affecting core paths (API Gateway or primary PostgreSQL cluster down). Page on-call immediately.
- **SEV-2**: Partial performance degradation or replication lag exceeding SLA thresholds.

## PostgreSQL Failover Runbook
1. **Health Verification**: Check primary availability via `kubectl exec -it pg-primary -- pg_isready`.
2. **Leader Election**: If primary fails health checks for > 60 seconds, initiate orchestrator failover.
3. **Standby Promotion**: Run `patronictl -c /etc/patroni.yml failover` to promote the leading replica.
4. **Traffic Verification**: Confirm write operations resume and replica lag converges below 50ms.
5. **Connection Pooler Redirection**: Verify PgBouncer connects to the newly promoted master.
