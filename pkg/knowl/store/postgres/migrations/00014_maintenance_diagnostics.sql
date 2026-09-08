-- +goose Up
ALTER TABLE knowl_operations ADD COLUMN maintenance_diagnostics TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE knowl_operations DROP COLUMN maintenance_diagnostics;
