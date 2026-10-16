-- +goose Up
ALTER TABLE knowl_embedding_state ADD COLUMN native_format INTEGER NOT NULL DEFAULT 0;
DELETE FROM knowl_embedding_state;

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='knowl_embedding_chunks'::regclass AND attname='vector' AND atttypid='bytea'::regtype AND NOT attisdropped) THEN
        RAISE EXCEPTION 'Roll back optional vector migrations before base migrations' USING ERRCODE='55000';
    END IF;
END $$;
-- +goose StatementEnd
DELETE FROM knowl_embedding_state;
ALTER TABLE knowl_embedding_state DROP COLUMN native_format;
