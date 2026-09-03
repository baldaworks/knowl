---
type: topic
title: Data Retention & Lifecycle
knowl:
  id: concepts/data-retention-and-lifecycle
  source_refs:
    - wiki-filesystem:engineering-docs/database-retention-policy.md@2b47dd12cbe3fd9c8da48712f72b0eedc5f6b8dda108e7eb5d8ecc271ad24590
---
# Data Retention & Lifecycle

Acme Cloud enforces scheduled lifecycle policies for data classification, compliance archiving, and automated purging.

## Data Classification and Schedules
- **Transactional Records**: Retained active in PostgreSQL for 2 years, then cold-archived as encrypted Parquet on object storage for 7 years.
- **Audit Logs**: Streamed to Write Once Read Many (WORM) storage with a 5-year retention period.
- **User Activity Logs**: Maintained in hot storage for 90 days, aggregated monthly for analytics, and purged after 180 days.

## Automated Purging and Compliance
- Daily cron jobs run chunked deletions during off-peak hours (02:00–04:00 UTC) with sleep pauses to mitigate replication lag.
- GDPR "Right to be Forgotten" requests trigger asynchronous cascade deletions across primary tables within 30 business days.
