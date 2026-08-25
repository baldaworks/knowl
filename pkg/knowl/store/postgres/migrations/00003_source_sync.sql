-- +goose Up
CREATE TABLE knowl_sources (
    scope TEXT NOT NULL, source_id TEXT NOT NULL, source_type TEXT NOT NULL,
    config_digest TEXT NOT NULL, checkpoint TEXT NOT NULL DEFAULT '',
    last_attempt_run_id TEXT NOT NULL DEFAULT '', last_success_run_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY(scope, source_id)
);
CREATE TABLE knowl_sync_runs (
    run_id TEXT PRIMARY KEY, scope TEXT NOT NULL, source_id TEXT NOT NULL, config_digest TEXT NOT NULL,
    status TEXT NOT NULL, cursor TEXT NOT NULL DEFAULT '', next_page_token TEXT NOT NULL DEFAULT '',
    complete_scan BOOLEAN NOT NULL DEFAULT FALSE,
    added BIGINT NOT NULL DEFAULT 0 CHECK (added >= 0), updated BIGINT NOT NULL DEFAULT 0 CHECK (updated >= 0),
    unchanged BIGINT NOT NULL DEFAULT 0 CHECK (unchanged >= 0), deleted BIGINT NOT NULL DEFAULT 0 CHECK (deleted >= 0),
    failed BIGINT NOT NULL DEFAULT 0 CHECK (failed >= 0), failure_class TEXT NOT NULL DEFAULT '',
    content_generation TEXT NOT NULL DEFAULT '', candidate_digest TEXT NOT NULL DEFAULT '', checkpoint TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL, completed_at TIMESTAMPTZ,
    FOREIGN KEY(scope, source_id) REFERENCES knowl_sources(scope, source_id)
);
CREATE INDEX knowl_sync_runs_scope_status ON knowl_sync_runs(scope, status, updated_at, run_id);
CREATE TABLE knowl_sync_seen (
    run_id TEXT NOT NULL REFERENCES knowl_sync_runs(run_id) ON DELETE CASCADE,
    document_id TEXT NOT NULL, revision TEXT NOT NULL, path TEXT NOT NULL, descriptor TEXT NOT NULL, ordinal INTEGER NOT NULL,
    PRIMARY KEY(run_id, document_id), UNIQUE(run_id, ordinal)
);
CREATE TABLE knowl_sync_candidates (
    run_id TEXT NOT NULL REFERENCES knowl_sync_runs(run_id) ON DELETE CASCADE,
    document_id TEXT NOT NULL, action TEXT NOT NULL CHECK (action IN ('active', 'tombstone')),
    revision TEXT NOT NULL, accepted_source TEXT NOT NULL, mirror_path TEXT NOT NULL DEFAULT '', mirror_digest TEXT NOT NULL DEFAULT '',
    last_seen_run_id TEXT NOT NULL, deleted_at TIMESTAMPTZ, candidate_digest TEXT NOT NULL,
    PRIMARY KEY(run_id, document_id)
);
CREATE TABLE knowl_source_documents (
    scope TEXT NOT NULL, source_id TEXT NOT NULL, document_id TEXT NOT NULL, revision TEXT NOT NULL,
    accepted_source TEXT NOT NULL, mirror_path TEXT NOT NULL DEFAULT '', mirror_digest TEXT NOT NULL DEFAULT '',
    last_seen_run_id TEXT NOT NULL, deleted BOOLEAN NOT NULL DEFAULT FALSE, deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY(scope, source_id, document_id), FOREIGN KEY(scope, source_id) REFERENCES knowl_sources(scope, source_id)
);
-- +goose Down
DROP TABLE knowl_source_documents;
DROP TABLE knowl_sync_candidates;
DROP TABLE knowl_sync_seen;
DROP INDEX knowl_sync_runs_scope_status;
DROP TABLE knowl_sync_runs;
DROP TABLE knowl_sources;
