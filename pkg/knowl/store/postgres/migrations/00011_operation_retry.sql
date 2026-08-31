-- +goose Up
ALTER TABLE knowl_operations ADD COLUMN failure_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE knowl_operations ADD COLUMN retry_attempt INTEGER NOT NULL DEFAULT 0 CHECK (retry_attempt >= 0);
ALTER TABLE knowl_operations ADD COLUMN manual_retry_count INTEGER NOT NULL DEFAULT 0 CHECK (manual_retry_count >= 0);

-- +goose Down
ALTER TABLE knowl_operations DROP COLUMN manual_retry_count;
ALTER TABLE knowl_operations DROP COLUMN retry_attempt;
ALTER TABLE knowl_operations DROP COLUMN failure_reason;
