-- +goose Up
ALTER TABLE knowl_pages ADD COLUMN wiki_paths TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE knowl_pages DROP COLUMN wiki_paths;
