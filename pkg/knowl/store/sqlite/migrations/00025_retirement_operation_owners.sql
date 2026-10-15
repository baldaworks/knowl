-- +goose Up
CREATE TABLE knowl_retirement_operation_owners (
    operation_id TEXT NOT NULL,
    scope TEXT NOT NULL,
    source_adapter TEXT NOT NULL,
    source_id TEXT NOT NULL,
    retirement_generation TEXT NOT NULL,
    page_id TEXT NOT NULL,
    PRIMARY KEY (operation_id, source_adapter, source_id),
    FOREIGN KEY (operation_id) REFERENCES knowl_operations(operation_id) ON DELETE CASCADE
);

CREATE INDEX idx_knowl_retirement_operation_owners_lookup
    ON knowl_retirement_operation_owners(scope, source_adapter, source_id, retirement_generation, operation_id);

INSERT INTO knowl_retirement_operation_owners (
    operation_id, scope, source_adapter, source_id, retirement_generation, page_id
)
SELECT operation_id, scope, source_adapter, source_id, source_digest, source_version
FROM knowl_operations
WHERE work_kind = 'source_retirement_page';

-- +goose Down
DROP TABLE knowl_retirement_operation_owners;
