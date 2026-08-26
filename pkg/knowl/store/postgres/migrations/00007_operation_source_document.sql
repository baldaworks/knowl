-- +goose Up
ALTER TABLE knowl_operations ADD COLUMN accepted_source_document TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE knowl_operations DROP COLUMN accepted_source_document;
