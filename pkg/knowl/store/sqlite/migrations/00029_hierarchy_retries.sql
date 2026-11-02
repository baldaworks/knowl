-- +goose Up
CREATE TABLE knowl_hierarchy_retries (
 operation_id TEXT NOT NULL REFERENCES knowl_operations(operation_id),
 work_attempt INTEGER NOT NULL CHECK (work_attempt > 0),
 manual_retry_count INTEGER NOT NULL CHECK (manual_retry_count > 0),
 failure_class TEXT NOT NULL,
 failure_reason TEXT NOT NULL,
 correction_report TEXT NOT NULL,
 failure_diagnostics TEXT NOT NULL,
 failed_at TEXT NOT NULL,
 retried_at TEXT NOT NULL,
 PRIMARY KEY (operation_id, work_attempt)
);

-- +goose Down
DROP TABLE knowl_hierarchy_retries;
