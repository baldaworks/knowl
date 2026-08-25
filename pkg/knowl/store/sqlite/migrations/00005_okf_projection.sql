-- +goose Up
ALTER TABLE knowl_pages ADD COLUMN format TEXT NOT NULL DEFAULT '';
ALTER TABLE knowl_pages ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE knowl_pages ADD COLUMN okf_metadata TEXT;

DROP TABLE knowl_pages_fts;
CREATE VIRTUAL TABLE knowl_pages_fts USING fts5(
    page_id UNINDEXED,
    scope UNINDEXED,
    path UNINDEXED,
    title,
    description,
    body,
    source_refs UNINDEXED
);

DELETE FROM knowl_projection_state;

-- +goose Down
DROP TABLE knowl_pages_fts;
CREATE VIRTUAL TABLE knowl_pages_fts USING fts5(
    page_id UNINDEXED,
    scope UNINDEXED,
    path UNINDEXED,
    title,
    body,
    source_refs UNINDEXED
);

ALTER TABLE knowl_pages DROP COLUMN okf_metadata;
ALTER TABLE knowl_pages DROP COLUMN description;
ALTER TABLE knowl_pages DROP COLUMN format;
