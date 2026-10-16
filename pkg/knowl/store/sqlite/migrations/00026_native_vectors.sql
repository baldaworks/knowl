-- +goose Up
ALTER TABLE knowl_embedding_state ADD COLUMN native_format INTEGER NOT NULL DEFAULT 0;
DELETE FROM knowl_embedding_state;

-- +goose Down
DELETE FROM knowl_embedding_state;
ALTER TABLE knowl_embedding_state DROP COLUMN native_format;
