---
type: topic
title: Data Retention & Lifecycle
knowl:
  id: concepts/data-retention-and-lifecycle
  source_refs:
    - wiki-filesystem:engineering-docs/database-retention-policy.md@2b47dd12cbe3fd9c8da48712f72b0eedc5f6b8dda108e7eb5d8ecc271ad24590
---
# Data Retention & Lifecycle

## Data Classification & Schedules
- **Transactional Records (Orders, Payments)**: Retained active in PostgreSQL for 2 years, then cold-archived as encrypted Parquet files in object storage for 7 years compliance.
- **Audit Logs**: Streamed to WORM (Write Once Read Many) immutable storage and retained for 5 years.
- **User Activity Logs**: Retained in hot storage for 90 days, aggregated monthly for analytics, and purged after 180 days.

## Automated Purging & Privacy
- **Scheduled Purging**: Daily cron jobs execute bounded chunked deletions during off-peak hours (02:00–04:00 UTC) with sleep intervals to prevent database replication lag.
- **GDPR Compliance**: "Right to be Forgotten" requests trigger asynchronous cascade deletion across primary tables within 30 business days.
