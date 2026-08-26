-- +goose Up
ALTER TABLE knowl_pages ADD COLUMN source_documents JSONB NOT NULL DEFAULT '[]'::jsonb;

CREATE TABLE knowl_page_sources (
    scope TEXT NOT NULL,
    page_id TEXT NOT NULL,
    source_id TEXT NOT NULL,
    document_id TEXT NOT NULL,
    revision TEXT NOT NULL,
    uri TEXT NOT NULL,
    PRIMARY KEY(scope, page_id, source_id, document_id, revision),
    FOREIGN KEY(scope, page_id) REFERENCES knowl_pages(scope, page_id) ON DELETE CASCADE
);

CREATE INDEX knowl_page_sources_filter_idx
    ON knowl_page_sources(scope, source_id, page_id);

-- +goose Down
DROP INDEX knowl_page_sources_filter_idx;
DROP TABLE knowl_page_sources;
ALTER TABLE knowl_pages DROP COLUMN source_documents;
