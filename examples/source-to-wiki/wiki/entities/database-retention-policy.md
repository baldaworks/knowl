---
id: entities/database-retention-policy
title: Database Retention Policy
type: entity
version: "0.2"
parent: catalogs/engineering
source_refs:
  - wiki-filesystem:engineering-docs/database-retention-policy.md
---
# Database Retention Policy

## Lifecycle & Retention Rules
- **Transactional Records**: Retained in active PostgreSQL storage for 2 years. Older partitions are converted to compressed parquet files and archived in object storage with a 7-year retention lifecycle.
- **Audit Logs**: Streamed directly to WORM (Write Once Read Many) compliant object storage and retained for 5 years.
- **User Activity Logs**: Stored hot for 90 days, aggregated into reporting tables, and purged after 180 days.

## Automated Cleanup Procedures
- Chunked cleanup cron jobs execute between 02:00 and 04:00 UTC with inter-batch sleep delays to avoid replica lag.
- GDPR erasure requests trigger asynchronous cascading deletion jobs across operational databases within 30 days.
