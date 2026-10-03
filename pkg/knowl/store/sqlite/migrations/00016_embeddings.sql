-- +goose Up
ALTER TABLE knowl_operations ADD COLUMN retrieval_report TEXT CHECK (retrieval_report IS NULL OR length(CAST(retrieval_report AS BLOB)) <= 2048);
ALTER TABLE knowl_operations ADD COLUMN retrieval_report_attempt INTEGER NOT NULL DEFAULT 0 CHECK (retrieval_report_attempt >= 0);

CREATE TABLE knowl_embedding_state (
    scope TEXT PRIMARY KEY REFERENCES knowl_projection_state(scope) ON DELETE CASCADE,
    space TEXT NOT NULL CHECK (length(CAST(space AS BLOB)) = 64),
    snapshot_digest TEXT NOT NULL CHECK (length(CAST(snapshot_digest AS BLOB)) = 64),
    dimensions INTEGER NOT NULL CHECK (dimensions BETWEEN 1 AND 4096),
    mode TEXT NOT NULL CHECK (mode IN ('hybrid', 'degraded')),
    reason TEXT NOT NULL DEFAULT '' CHECK (length(CAST(reason AS BLOB)) <= 64),
    chunk_count INTEGER NOT NULL CHECK (chunk_count BETWEEN 0 AND 8192),
    omitted_chunks INTEGER NOT NULL CHECK (omitted_chunks >= 0),
    omitted_runes INTEGER NOT NULL CHECK (omitted_runes >= 0),
    ready_at TEXT NOT NULL CHECK (length(CAST(ready_at AS BLOB)) BETWEEN 1 AND 64)
);
CREATE TABLE knowl_embedding_chunks (
    scope TEXT NOT NULL REFERENCES knowl_embedding_state(scope) ON DELETE CASCADE,
    space TEXT NOT NULL CHECK (length(CAST(space AS BLOB)) = 64),
    page_id TEXT NOT NULL CHECK (length(CAST(page_id AS BLOB)) BETWEEN 1 AND 4096),
    page_digest TEXT NOT NULL CHECK (length(CAST(page_digest AS BLOB)) BETWEEN 1 AND 256),
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 0 AND 15),
    content_hash TEXT NOT NULL CHECK (length(CAST(content_hash AS BLOB)) = 64),
    dimensions INTEGER NOT NULL CHECK (dimensions BETWEEN 1 AND 4096),
    vector BLOB NOT NULL CHECK (length(vector) = dimensions * 4),
    PRIMARY KEY(scope, space, page_id, ordinal),
    FOREIGN KEY(scope, page_id) REFERENCES knowl_pages(scope, page_id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE knowl_embedding_chunks;
DROP TABLE knowl_embedding_state;
ALTER TABLE knowl_operations DROP COLUMN retrieval_report_attempt;
ALTER TABLE knowl_operations DROP COLUMN retrieval_report;
