---
type: topic
title: Incident Response
knowl:
  id: concepts/incident-response
  source_refs:
    - wiki-filesystem:engineering-docs/incident-response-runbook.md@d1a320d561ec3d45814a98010fab458e32e159de36037290aebd79981d20d110
---
# Incident Response

Immediate actions and operational procedures for high-severity incidents and database outages across the [[entities/acme-cloud-platform]].

## Triage & Severity Levels
- **SEV-1**: Core service outage ([[entities/api-gateway]] or Primary DB unreachable). On-call engineer alerted immediately via PagerDuty.
- **SEV-2**: Partial degradation (read replicas lagging or elevated error rates > 2%).

## PostgreSQL Failover Procedure
1. Verify primary node health: `kubectl exec -it pg-primary -- pg_isready`.
2. If primary node is unresponsive for > 60 seconds, initiate Patroni / orchestrator leader election.
3. Promote designated standby replica: `patronictl -c /etc/patroni.yml failover`.
4. Validate read-write traffic resumes and replica replication lag converges to < 50ms.
5. Re-route database connection poolers (PgBouncer) if DNS cutover does not propagate within 30 seconds.

## Communication
- Post incident notification to `#incidents-live` Slack channel within 10 minutes of SEV-1 declaration.
