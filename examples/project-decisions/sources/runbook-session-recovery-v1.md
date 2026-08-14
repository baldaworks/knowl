# Runbook: session recovery

Restart the sidecar, poll the durable operation, verify its recovery lease,
and rebuild projections when required. Keep the session-memory decision linked
from the recovery procedure.
