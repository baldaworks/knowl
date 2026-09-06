---
type: topic
title: Data Lifecycle
knowl:
  id: concepts/data-lifecycle
  source_refs:
    - wiki-filesystem:engineering-docs/database-retention-policy.md@2b47dd12cbe3fd9c8da48712f72b0eedc5f6b8dda108e7eb5d8ecc271ad24590
---
# Data Lifecycle

This policy governs data retention, archiving schedules, and automated purging across the [[entities/acme-cloud-platform]].

## Data Classification and Schedules
- **Transactional Records (Orders, Payments)**: Retained active in PostgreSQL for 2 years, then cold-archived to encrypted parquet on object storage for 7 years compliance.
- **Audit Logs**: Immutable audit trails are streamed to WORM (Write Once Read Many) storage and retained for 5 years.
- **User Activity Logs**: Maintained in hot storage for 90 days, aggregated into monthly analytics, and purged after 180 days.

## Automated Purging
- Daily cron jobs execute bounded chunked deletions during off-peak hours (02:00-04:00 UTC) with sleep pauses to avoid database replication lag.
- GDPR "Right to be Forgotten" requests trigger an asynchronous cascade deletion job across primary tables within 30 business days.
