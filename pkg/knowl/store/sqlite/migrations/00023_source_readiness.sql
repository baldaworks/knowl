-- +goose Up
ALTER TABLE knowl_sync_runs ADD COLUMN maintenance_ready INTEGER NOT NULL DEFAULT 0 CHECK (maintenance_ready IN (0, 1));
ALTER TABLE knowl_sync_runs ADD COLUMN prepared_digest_version INTEGER NOT NULL DEFAULT 0 CHECK (prepared_digest_version IN (0, 1));
ALTER TABLE knowl_source_lifecycle ADD COLUMN replacement_pending INTEGER NOT NULL DEFAULT 0 CHECK (replacement_pending BETWEEN 0 AND 128);

-- +goose Down
ALTER TABLE knowl_source_lifecycle DROP COLUMN replacement_pending;
ALTER TABLE knowl_sync_runs DROP COLUMN prepared_digest_version;
ALTER TABLE knowl_sync_runs DROP COLUMN maintenance_ready;
