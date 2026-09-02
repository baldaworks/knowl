# Database Lifecycle & Retention Policy

This policy governs the retention, archiving, and purging of corporate and user data.

## 1. Data Classification & Schedules
- **Transactional Records (Orders, Payments)**: Retained active in PostgreSQL for 2 years, then cold-archived to encrypted parquet on object storage for 7 years compliance.
- **Audit Logs**: Immutable audit trails are streamed to WORM (Write Once Read Many) storage and retained for 5 years.
- **User Activity Logs**: Maintained in hot storage for 90 days, aggregated into monthly analytics, and purged after 180 days.

## 2. Automated Purging
- Daily cron jobs execute bounded chunked deletions during off-peak hours (02:00-04:00 UTC) with sleep pauses to avoid database replication lag.
- GDPR "Right to be Forgotten" requests trigger an asynchronous cascade deletion job across primary tables within 30 business days.
