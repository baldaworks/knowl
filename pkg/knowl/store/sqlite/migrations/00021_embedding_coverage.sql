-- +goose Up
ALTER TABLE knowl_embedding_state ADD COLUMN coverage TEXT NOT NULL DEFAULT '' CHECK (length(CAST(coverage AS BLOB)) <= 1048576);
CREATE TABLE knowl_embedding_chunks_new (
    scope TEXT NOT NULL REFERENCES knowl_embedding_state(scope) ON DELETE CASCADE,
    space TEXT NOT NULL CHECK (length(CAST(space AS BLOB)) = 64),
    page_id TEXT NOT NULL CHECK (length(CAST(page_id AS BLOB)) BETWEEN 1 AND 4096),
    page_digest TEXT NOT NULL CHECK (length(CAST(page_digest AS BLOB)) BETWEEN 1 AND 256),
    ordinal INTEGER NOT NULL CHECK (ordinal BETWEEN 0 AND 8191),
    content_hash TEXT NOT NULL CHECK (length(CAST(content_hash AS BLOB)) = 64),
    dimensions INTEGER NOT NULL CHECK (dimensions BETWEEN 1 AND 4096),
    vector BLOB NOT NULL CHECK (length(vector) = dimensions * 4),
    PRIMARY KEY(scope, space, page_id, ordinal),
    FOREIGN KEY(scope, page_id) REFERENCES knowl_pages(scope, page_id) ON DELETE CASCADE
);
INSERT INTO knowl_embedding_chunks_new SELECT * FROM knowl_embedding_chunks;
DROP TABLE knowl_embedding_chunks;
ALTER TABLE knowl_embedding_chunks_new RENAME TO knowl_embedding_chunks;

-- +goose Down
CREATE TABLE knowl_embedding_chunks_old (
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
INSERT INTO knowl_embedding_chunks_old SELECT * FROM knowl_embedding_chunks WHERE ordinal <= 15;
DROP TABLE knowl_embedding_chunks;
ALTER TABLE knowl_embedding_chunks_old RENAME TO knowl_embedding_chunks;
ALTER TABLE knowl_embedding_state DROP COLUMN coverage;
