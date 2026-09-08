-- +goose Up
-- SQLite cannot replace a table-level UNIQUE constraint in place.
ALTER TABLE knowl_operations RENAME TO knowl_operations_legacy_generation;

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
    accepted_media_type TEXT NOT NULL DEFAULT '',
    source_manifest_ref TEXT NOT NULL DEFAULT '',
    schema_version TEXT NOT NULL DEFAULT '',
    schema_snapshot BLOB,
    work_attempt INTEGER NOT NULL DEFAULT 0,
    work_lease_token TEXT NOT NULL DEFAULT '',
    work_lease_expires_at TEXT NOT NULL DEFAULT '',
    work_ready_at TEXT NOT NULL DEFAULT '',
    accepted_source_document TEXT NOT NULL DEFAULT '',
    work_kind TEXT NOT NULL DEFAULT 'source',
    execution_payload TEXT NOT NULL DEFAULT '',
    failure_reason TEXT NOT NULL DEFAULT '',
    retry_attempt INTEGER NOT NULL DEFAULT 0 CHECK (retry_attempt >= 0),
    manual_retry_count INTEGER NOT NULL DEFAULT 0 CHECK (manual_retry_count >= 0),
    maintenance_generation TEXT NOT NULL DEFAULT '',
    UNIQUE(scope, source_adapter, source_id, source_version, maintenance_generation)
);

INSERT INTO knowl_operations (
    operation_id, scope, source_adapter, source_id, source_version, source_digest,
    schema_digest, status, attempt, plan_digest, failure_class, commit_generation,
    lease_token, lease_expires_at, created_at, updated_at, accepted_media_type,
    source_manifest_ref, schema_version, schema_snapshot, work_attempt,
    work_lease_token, work_lease_expires_at, work_ready_at, accepted_source_document,
    work_kind, execution_payload, failure_reason, retry_attempt, manual_retry_count,
    maintenance_generation
)
SELECT
    operation_id, scope, source_adapter, source_id, source_version, source_digest,
    schema_digest, status, attempt, plan_digest, failure_class, commit_generation,
    lease_token, lease_expires_at, created_at, updated_at, accepted_media_type,
    source_manifest_ref, schema_version, schema_snapshot, work_attempt,
    work_lease_token, work_lease_expires_at, work_ready_at, accepted_source_document,
    work_kind, execution_payload, failure_reason, retry_attempt, manual_retry_count, ''
FROM knowl_operations_legacy_generation;

DROP TABLE knowl_operations_legacy_generation;

CREATE INDEX knowl_operations_scope_status ON knowl_operations(scope, status, updated_at);
CREATE INDEX knowl_operations_scope_work_ready
    ON knowl_operations(scope, status, work_ready_at, work_lease_expires_at, operation_id);
CREATE INDEX knowl_operations_scope_kind_ready
    ON knowl_operations(scope, work_kind, status, work_ready_at, work_lease_expires_at, operation_id);

ALTER TABLE knowl_sync_candidates ADD COLUMN maintenance_generation TEXT NOT NULL DEFAULT '';
ALTER TABLE knowl_source_documents ADD COLUMN maintenance_generation TEXT NOT NULL DEFAULT '';

-- +goose Down
-- Copying into the old UNIQUE shape intentionally fails if generation-bearing
-- history cannot be represented without loss.
ALTER TABLE knowl_operations RENAME TO knowl_operations_generation;

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
    accepted_media_type TEXT NOT NULL DEFAULT '',
    source_manifest_ref TEXT NOT NULL DEFAULT '',
    schema_version TEXT NOT NULL DEFAULT '',
    schema_snapshot BLOB,
    work_attempt INTEGER NOT NULL DEFAULT 0,
    work_lease_token TEXT NOT NULL DEFAULT '',
    work_lease_expires_at TEXT NOT NULL DEFAULT '',
    work_ready_at TEXT NOT NULL DEFAULT '',
    accepted_source_document TEXT NOT NULL DEFAULT '',
    work_kind TEXT NOT NULL DEFAULT 'source',
    execution_payload TEXT NOT NULL DEFAULT '',
    failure_reason TEXT NOT NULL DEFAULT '',
    retry_attempt INTEGER NOT NULL DEFAULT 0 CHECK (retry_attempt >= 0),
    manual_retry_count INTEGER NOT NULL DEFAULT 0 CHECK (manual_retry_count >= 0),
    UNIQUE(scope, source_adapter, source_id, source_version)
);

INSERT INTO knowl_operations (
    operation_id, scope, source_adapter, source_id, source_version, source_digest,
    schema_digest, status, attempt, plan_digest, failure_class, commit_generation,
    lease_token, lease_expires_at, created_at, updated_at, accepted_media_type,
    source_manifest_ref, schema_version, schema_snapshot, work_attempt,
    work_lease_token, work_lease_expires_at, work_ready_at, accepted_source_document,
    work_kind, execution_payload, failure_reason, retry_attempt, manual_retry_count
)
SELECT
    operation_id, scope, source_adapter, source_id, source_version, source_digest,
    schema_digest, status, attempt, plan_digest, failure_class, commit_generation,
    lease_token, lease_expires_at, created_at, updated_at, accepted_media_type,
    source_manifest_ref, schema_version, schema_snapshot, work_attempt,
    work_lease_token, work_lease_expires_at, work_ready_at, accepted_source_document,
    work_kind, execution_payload, failure_reason, retry_attempt, manual_retry_count
FROM knowl_operations_generation;

DROP TABLE knowl_operations_generation;

CREATE INDEX knowl_operations_scope_status ON knowl_operations(scope, status, updated_at);
CREATE INDEX knowl_operations_scope_work_ready
    ON knowl_operations(scope, status, work_ready_at, work_lease_expires_at, operation_id);
CREATE INDEX knowl_operations_scope_kind_ready
    ON knowl_operations(scope, work_kind, status, work_ready_at, work_lease_expires_at, operation_id);

ALTER TABLE knowl_sync_candidates DROP COLUMN maintenance_generation;
ALTER TABLE knowl_source_documents DROP COLUMN maintenance_generation;
