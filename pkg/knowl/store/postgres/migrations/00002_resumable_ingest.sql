-- +goose Up
ALTER TABLE knowl_operations ADD COLUMN accepted_media_type TEXT NOT NULL DEFAULT '';
ALTER TABLE knowl_operations ADD COLUMN source_manifest_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE knowl_operations ADD COLUMN schema_version TEXT NOT NULL DEFAULT '';
ALTER TABLE knowl_operations ADD COLUMN schema_snapshot BYTEA;
ALTER TABLE knowl_operations ADD COLUMN work_attempt INTEGER NOT NULL DEFAULT 0;
ALTER TABLE knowl_operations ADD COLUMN work_lease_token TEXT NOT NULL DEFAULT '';
ALTER TABLE knowl_operations ADD COLUMN work_lease_expires_at TIMESTAMPTZ;
ALTER TABLE knowl_operations ADD COLUMN work_ready_at TIMESTAMPTZ;

UPDATE knowl_operations SET work_ready_at = created_at WHERE work_ready_at IS NULL;
ALTER TABLE knowl_operations ALTER COLUMN work_ready_at SET NOT NULL;

CREATE INDEX knowl_operations_scope_work_ready
    ON knowl_operations(scope, status, work_ready_at, work_lease_expires_at, operation_id);

-- +goose Down
DROP INDEX knowl_operations_scope_work_ready;
ALTER TABLE knowl_operations DROP COLUMN work_ready_at;
ALTER TABLE knowl_operations DROP COLUMN work_lease_expires_at;
ALTER TABLE knowl_operations DROP COLUMN work_lease_token;
ALTER TABLE knowl_operations DROP COLUMN work_attempt;
ALTER TABLE knowl_operations DROP COLUMN schema_snapshot;
ALTER TABLE knowl_operations DROP COLUMN schema_version;
ALTER TABLE knowl_operations DROP COLUMN source_manifest_ref;
ALTER TABLE knowl_operations DROP COLUMN accepted_media_type;
