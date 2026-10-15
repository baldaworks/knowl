-- +goose Up
ALTER TABLE knowl_operations ADD COLUMN failure_diagnostics TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE knowl_operations DROP COLUMN failure_diagnostics;
