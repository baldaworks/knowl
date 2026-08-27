-- +goose Up
-- Existing rows remain source work. Once hierarchy rows exist, downgrading
-- below this migration is unsupported until those rows are removed.
ALTER TABLE knowl_operations ADD COLUMN work_kind TEXT NOT NULL DEFAULT 'source';
ALTER TABLE knowl_operations ADD COLUMN execution_payload TEXT NOT NULL DEFAULT '';

CREATE INDEX knowl_operations_scope_kind_ready
    ON knowl_operations(scope, work_kind, status, work_ready_at, work_lease_expires_at, operation_id);

-- +goose Down
DROP INDEX knowl_operations_scope_kind_ready;
ALTER TABLE knowl_operations DROP COLUMN execution_payload;
ALTER TABLE knowl_operations DROP COLUMN work_kind;
