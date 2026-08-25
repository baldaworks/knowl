-- +goose Up
ALTER TABLE knowl_pages ADD COLUMN source_id TEXT;
ALTER TABLE knowl_pages ADD COLUMN source_document JSONB;
CREATE INDEX knowl_pages_source_idx ON knowl_pages(scope, source_id) WHERE source_id IS NOT NULL;

-- +goose Down
DROP INDEX knowl_pages_source_idx;
ALTER TABLE knowl_pages DROP COLUMN source_document;
ALTER TABLE knowl_pages DROP COLUMN source_id;
