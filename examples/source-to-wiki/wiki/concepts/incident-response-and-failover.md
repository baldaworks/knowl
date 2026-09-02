---
type: topic
title: Incident Response & Failover
knowl:
  id: concepts/incident-response-and-failover
  source_refs:
    - wiki-filesystem:engineering-docs/incident-response-runbook.md@d1a320d561ec3d45814a98010fab458e32e159de36037290aebd79981d20d110
---
# Incident Response & Failover

## Triage & Severity Levels
- **SEV-1**: Core service outage (API Gateway or Primary DB unreachable). Alerts on-call engineer immediately via PagerDuty.
- **SEV-2**: Partial degradation (read replicas lagging or elevated error rates > 2%).

## PostgreSQL Failover Procedure
1. Verify primary node health: `kubectl exec -it pg-primary -- pg_isready`.
2. If the primary node remains unresponsive for > 60 seconds, initiate Patroni leader election.
3. Promote the designated standby replica using `patronictl -c /etc/patroni.yml failover`.
4. Validate resumption of read-write traffic and replication lag convergence to < 50ms.
5. Re-route PgBouncer database connection poolers if DNS cutover does not propagate within 30 seconds.

## Communication Protocols
- Post incident notifications to `#incidents-live` Slack channel within 10 minutes of SEV-1 declaration.
