-- +goose Up
CREATE TABLE knowl_operations (
    operation_id TEXT PRIMARY KEY,
    scope TEXT NOT NULL,
    source_adapter TEXT NOT NULL,
    source_id TEXT NOT NULL,
    source_version TEXT NOT NULL,
    source_digest TEXT NOT NULL,
    schema_digest TEXT NOT NULL,
    status TEXT NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 0,
    plan_digest TEXT NOT NULL DEFAULT '',
    failure_class TEXT NOT NULL DEFAULT '',
    commit_generation TEXT NOT NULL DEFAULT '',
    lease_token TEXT NOT NULL DEFAULT '',
    lease_expires_at TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(scope, source_adapter, source_id, source_version)
);

CREATE INDEX knowl_operations_scope_status ON knowl_operations(scope, status, updated_at);

CREATE TABLE knowl_pages (
    scope TEXT NOT NULL,
    page_id TEXT NOT NULL,
    path TEXT NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    digest TEXT NOT NULL,
    source_refs TEXT NOT NULL DEFAULT '[]',
    updated_at TEXT NOT NULL,
    PRIMARY KEY(scope, page_id),
    UNIQUE(scope, path)
);

CREATE TABLE knowl_links (
    scope TEXT NOT NULL,
    from_page TEXT NOT NULL,
    to_page TEXT NOT NULL,
    relation TEXT NOT NULL,
    PRIMARY KEY(scope, from_page, to_page, relation)
);

CREATE INDEX knowl_links_scope_from ON knowl_links(scope, from_page);
CREATE INDEX knowl_links_scope_to ON knowl_links(scope, to_page);

CREATE TABLE knowl_projection_state (
    scope TEXT PRIMARY KEY,
    schema_digest TEXT NOT NULL,
    snapshot_digest TEXT NOT NULL,
    page_count INTEGER NOT NULL,
    link_count INTEGER NOT NULL,
    ready_at TEXT NOT NULL
);

CREATE VIRTUAL TABLE knowl_pages_fts USING fts5(
    page_id UNINDEXED,
    scope UNINDEXED,
    path UNINDEXED,
    title,
    body,
    source_refs UNINDEXED
);

-- +goose Down
DROP TABLE knowl_pages_fts;
DROP TABLE knowl_projection_state;
DROP TABLE knowl_links;
DROP TABLE knowl_pages;
DROP TABLE knowl_operations;
