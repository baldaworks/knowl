# Incident Response & Failover Runbook

This runbook describes immediate actions for primary database outages and high-severity incidents.

## 1. Triage & Severity Levels
- **SEV-1**: Core service outage (API Gateway or Primary DB unreachable). On-call engineer alerted immediately via PagerDuty.
- **SEV-2**: Partial degradation (read replicas lagging or elevated error rates > 2%).

## 2. PostgreSQL Failover Procedure
1. Verify primary node health: `kubectl exec -it pg-primary -- pg_isready`.
2. If primary node is unresponsive for > 60 seconds, initiate Patroni / orchestrator leader election.
3. Promote designated standby replica: `patronictl -c /etc/patroni.yml failover`.
4. Validate read-write traffic resumes and replica replication lag converges to < 50ms.
5. Re-route database connection poolers (PgBouncer) if DNS cutover does not propagate within 30 seconds.

## 3. Communication
- Post incident notification to `#incidents-live` Slack channel within 10 minutes of SEV-1 declaration.
