-- +goose Up
-- The existing primary key follows the database's default document collation.
-- Operator continuation uses bytewise order consistently across both stores.
CREATE INDEX knowl_source_documents_operator_key
    ON knowl_source_documents(scope, source_id, document_id COLLATE "C");

-- +goose Down
DROP INDEX knowl_source_documents_operator_key;
