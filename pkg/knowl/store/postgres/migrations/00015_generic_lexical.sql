-- +goose Up
ALTER TABLE knowl_pages ALTER COLUMN search_vector DROP EXPRESSION;
ALTER TABLE knowl_pages ALTER COLUMN search_vector SET DEFAULT ''::tsvector;
UPDATE knowl_pages SET search_vector = ''::tsvector;
DELETE FROM knowl_projection_state;

-- +goose Down
DROP INDEX knowl_pages_search;
ALTER TABLE knowl_pages DROP COLUMN search_vector;
ALTER TABLE knowl_pages ADD COLUMN search_vector TSVECTOR GENERATED ALWAYS AS (
    setweight(to_tsvector('simple'::regconfig, coalesce(title, '')), 'A') ||
    setweight(to_tsvector('simple'::regconfig, coalesce(tags, '')), 'B') ||
    setweight(to_tsvector('simple'::regconfig, coalesce(description, '')), 'C') ||
    setweight(to_tsvector('simple'::regconfig, coalesce(body, '')), 'D')
) STORED;
CREATE INDEX knowl_pages_search ON knowl_pages USING GIN(search_vector);
DELETE FROM knowl_projection_state;
