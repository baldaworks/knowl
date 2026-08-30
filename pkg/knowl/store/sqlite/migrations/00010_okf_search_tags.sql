-- +goose Up
ALTER TABLE knowl_pages ADD COLUMN tags TEXT NOT NULL DEFAULT '';

DROP TABLE knowl_pages_fts;
CREATE VIRTUAL TABLE knowl_pages_fts USING fts5(
    page_id UNINDEXED,
    scope UNINDEXED,
    path UNINDEXED,
    title,
    tags,
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
    description,
    body,
    source_refs UNINDEXED
);

ALTER TABLE knowl_pages DROP COLUMN tags;
DELETE FROM knowl_projection_state;
