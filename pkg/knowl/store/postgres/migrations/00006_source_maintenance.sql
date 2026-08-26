-- +goose Up
ALTER TABLE knowl_sync_candidates ADD COLUMN maintenance_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE knowl_sync_candidates ADD COLUMN maintenance_operation_id TEXT NOT NULL DEFAULT '';
ALTER TABLE knowl_source_documents ADD COLUMN maintenance_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE knowl_source_documents ADD COLUMN maintenance_operation_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE knowl_source_documents DROP COLUMN maintenance_operation_id;
ALTER TABLE knowl_source_documents DROP COLUMN maintenance_revision;
ALTER TABLE knowl_sync_candidates DROP COLUMN maintenance_operation_id;
ALTER TABLE knowl_sync_candidates DROP COLUMN maintenance_revision;
