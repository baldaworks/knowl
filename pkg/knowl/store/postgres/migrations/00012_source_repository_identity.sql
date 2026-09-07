-- +goose Up
ALTER TABLE knowl_sources ADD COLUMN repository_identity TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE knowl_sources DROP COLUMN repository_identity;
