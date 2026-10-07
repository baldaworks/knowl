-- +goose Up
ALTER TABLE knowl_embedding_state ADD COLUMN coverage TEXT NOT NULL DEFAULT '' CHECK (octet_length(coverage) <= 1048576);
ALTER TABLE knowl_embedding_chunks DROP CONSTRAINT knowl_embedding_chunks_ordinal_check;
ALTER TABLE knowl_embedding_chunks ADD CONSTRAINT knowl_embedding_chunks_ordinal_check CHECK (ordinal BETWEEN 0 AND 8191);

-- +goose Down
DELETE FROM knowl_embedding_chunks WHERE ordinal > 15;
ALTER TABLE knowl_embedding_chunks DROP CONSTRAINT knowl_embedding_chunks_ordinal_check;
ALTER TABLE knowl_embedding_chunks ADD CONSTRAINT knowl_embedding_chunks_ordinal_check CHECK (ordinal BETWEEN 0 AND 15);
ALTER TABLE knowl_embedding_state DROP COLUMN coverage;
