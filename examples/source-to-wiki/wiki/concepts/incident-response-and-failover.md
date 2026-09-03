---
type: topic
title: Incident Response & Failover
knowl:
  id: concepts/incident-response-and-failover
  source_refs:
    - wiki-filesystem:engineering-docs/incident-response-runbook.md@d1a320d561ec3d45814a98010fab458e32e159de36037290aebd79981d20d110
---
# Incident Response & Failover

Incident triage classifications and automated/manual failover runbooks ensure high availability across core services and data stores.

## Incident Severity Classifications
- **SEV-1**: Core service outage such as API Gateway or Primary Database unreachability. On-call engineers are alerted immediately via PagerDuty. Status updates are posted to `#incidents-live` within 10 minutes.
- **SEV-2**: Partial service degradation, including elevated error rates (> 2%) or replica replication lag.

## PostgreSQL Database Failover
1. Check primary health via `kubectl exec -it pg-primary -- pg_isready`.
2. If primary node is unresponsive for > 60 seconds, initiate Patroni leader election and promote designated standby (`patronictl -c /etc/patroni.yml failover`).
3. Verify read-write traffic resumes and replication lag converges to < 50ms.
4. Re-route PgBouncer connection poolers if DNS cutover does not propagate within 30 seconds.
