-- +goose Up
ALTER TABLE knowl_operations ADD COLUMN maintenance_generation TEXT NOT NULL DEFAULT '';
ALTER TABLE knowl_operations DROP CONSTRAINT knowl_operations_scope_source_adapter_source_id_source_vers_key;
ALTER TABLE knowl_operations ADD CONSTRAINT knowl_operations_source_generation_key
    UNIQUE(scope, source_adapter, source_id, source_version, maintenance_generation);

ALTER TABLE knowl_sync_candidates ADD COLUMN maintenance_generation TEXT NOT NULL DEFAULT '';
ALTER TABLE knowl_source_documents ADD COLUMN maintenance_generation TEXT NOT NULL DEFAULT '';

-- +goose Down
-- This intentionally fails rather than discard history when more than one
-- generation exists for a source revision. Stop new writers before downgrade.
ALTER TABLE knowl_operations DROP CONSTRAINT knowl_operations_source_generation_key;
ALTER TABLE knowl_operations ADD CONSTRAINT knowl_operations_scope_source_adapter_source_id_source_vers_key
    UNIQUE(scope, source_adapter, source_id, source_version);
ALTER TABLE knowl_operations DROP COLUMN maintenance_generation;

ALTER TABLE knowl_sync_candidates DROP COLUMN maintenance_generation;
ALTER TABLE knowl_source_documents DROP COLUMN maintenance_generation;
