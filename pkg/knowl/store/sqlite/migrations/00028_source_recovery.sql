-- +goose Up
ALTER TABLE knowl_source_lifecycle RENAME TO knowl_source_lifecycle_before_recovery;
CREATE TABLE knowl_source_lifecycle (
    scope TEXT NOT NULL,
    adapter TEXT COLLATE BINARY NOT NULL CHECK (adapter IN ('wiki-filesystem', 'git')),
    source_id TEXT COLLATE BINARY NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('active', 'retiring', 'deleted')),
    config_digest TEXT NOT NULL,
    authority_generation TEXT NOT NULL CHECK (length(authority_generation) = 64),
    authority_epoch INTEGER NOT NULL DEFAULT 0 CHECK (authority_epoch >= 0),
    activated_generation TEXT NOT NULL CHECK (length(activated_generation) = 64),
    activated_epoch INTEGER NOT NULL DEFAULT 0 CHECK (activated_epoch >= 0),
    retirement_generation TEXT NOT NULL DEFAULT '' CHECK (retirement_generation = '' OR length(retirement_generation) = 64),
    retirement_epoch INTEGER NOT NULL DEFAULT 0 CHECK (retirement_epoch >= 0),
    scan_cursor TEXT NOT NULL DEFAULT '' CHECK (length(CAST(scan_cursor AS BLOB)) <= 4096),
    scan_round_matches INTEGER NOT NULL DEFAULT 0 CHECK (scan_round_matches >= 0),
    scan_complete INTEGER NOT NULL DEFAULT 0 CHECK (scan_complete IN (0, 1)),
    ready INTEGER NOT NULL DEFAULT 0 CHECK (ready IN (0, 1)),
    blocker_code TEXT NOT NULL DEFAULT '' CHECK (length(CAST(blocker_code AS BLOB)) <= 128),
    activated_at TEXT NOT NULL,
    retiring_at TEXT NOT NULL DEFAULT '',
    deleted_at TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    replacement_pending INTEGER NOT NULL DEFAULT 0 CHECK (replacement_pending BETWEEN 0 AND 128),
    recovery_proof_payload TEXT NOT NULL DEFAULT '' CHECK (length(CAST(recovery_proof_payload AS BLOB)) <= 65536),
    recovery_proof_digest TEXT NOT NULL DEFAULT '' CHECK (recovery_proof_digest = '' OR length(recovery_proof_digest) = 64),
    CHECK ((length(config_digest) = 64 AND recovery_proof_digest = '') OR (config_digest = '' AND length(recovery_proof_digest) = 64 AND state IN ('retiring', 'deleted') AND recovery_proof_payload <> '')),
    PRIMARY KEY(scope, adapter, source_id)
);

INSERT INTO knowl_source_lifecycle (scope, adapter, source_id, state, config_digest, authority_generation, authority_epoch, activated_generation, activated_epoch, retirement_generation, retirement_epoch, scan_cursor, scan_round_matches, scan_complete, ready, blocker_code, activated_at, retiring_at, deleted_at, updated_at, replacement_pending) SELECT scope, adapter, source_id, state, config_digest, authority_generation, authority_epoch, activated_generation, activated_epoch, retirement_generation, retirement_epoch, scan_cursor, scan_round_matches, scan_complete, ready, blocker_code, activated_at, retiring_at, deleted_at, updated_at, replacement_pending FROM knowl_source_lifecycle_before_recovery;
DROP TABLE knowl_source_lifecycle_before_recovery;
CREATE INDEX knowl_source_lifecycle_scope_state ON knowl_source_lifecycle(scope, state, adapter, source_id);
CREATE INDEX knowl_source_lifecycle_scan_eligibility ON knowl_source_lifecycle(scope, state, scan_complete, adapter, source_id);

-- +goose Down
-- Do not erase even historical proof retained after genuine activation.
CREATE TEMP TABLE knowl_source_recovery_down_guard (proof_count INTEGER NOT NULL CHECK (proof_count = 0));
INSERT INTO knowl_source_recovery_down_guard SELECT count(*) FROM knowl_source_lifecycle WHERE recovery_proof_payload <> '';
DROP TABLE knowl_source_recovery_down_guard;
ALTER TABLE knowl_source_lifecycle RENAME TO knowl_source_lifecycle_with_recovery;
CREATE TABLE knowl_source_lifecycle (
    scope TEXT NOT NULL,
    adapter TEXT COLLATE BINARY NOT NULL CHECK (adapter IN ('wiki-filesystem', 'git')),
    source_id TEXT COLLATE BINARY NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('active', 'retiring', 'deleted')),
    config_digest TEXT NOT NULL CHECK (length(config_digest) = 64),
    authority_generation TEXT NOT NULL CHECK (length(authority_generation) = 64),
    authority_epoch INTEGER NOT NULL DEFAULT 0 CHECK (authority_epoch >= 0),
    activated_generation TEXT NOT NULL CHECK (length(activated_generation) = 64),
    activated_epoch INTEGER NOT NULL DEFAULT 0 CHECK (activated_epoch >= 0),
    retirement_generation TEXT NOT NULL DEFAULT '' CHECK (retirement_generation = '' OR length(retirement_generation) = 64),
    retirement_epoch INTEGER NOT NULL DEFAULT 0 CHECK (retirement_epoch >= 0),
    scan_cursor TEXT NOT NULL DEFAULT '' CHECK (length(CAST(scan_cursor AS BLOB)) <= 4096),
    scan_round_matches INTEGER NOT NULL DEFAULT 0 CHECK (scan_round_matches >= 0),
    scan_complete INTEGER NOT NULL DEFAULT 0 CHECK (scan_complete IN (0, 1)),
    ready INTEGER NOT NULL DEFAULT 0 CHECK (ready IN (0, 1)),
    blocker_code TEXT NOT NULL DEFAULT '' CHECK (length(CAST(blocker_code AS BLOB)) <= 128),
    activated_at TEXT NOT NULL,
    retiring_at TEXT NOT NULL DEFAULT '',
    deleted_at TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    replacement_pending INTEGER NOT NULL DEFAULT 0 CHECK (replacement_pending BETWEEN 0 AND 128),
    PRIMARY KEY(scope, adapter, source_id)
);

INSERT INTO knowl_source_lifecycle (scope, adapter, source_id, state, config_digest, authority_generation, authority_epoch, activated_generation, activated_epoch, retirement_generation, retirement_epoch, scan_cursor, scan_round_matches, scan_complete, ready, blocker_code, activated_at, retiring_at, deleted_at, updated_at, replacement_pending) SELECT scope, adapter, source_id, state, config_digest, authority_generation, authority_epoch, activated_generation, activated_epoch, retirement_generation, retirement_epoch, scan_cursor, scan_round_matches, scan_complete, ready, blocker_code, activated_at, retiring_at, deleted_at, updated_at, replacement_pending FROM knowl_source_lifecycle_with_recovery;
DROP TABLE knowl_source_lifecycle_with_recovery;
CREATE INDEX knowl_source_lifecycle_scope_state ON knowl_source_lifecycle(scope, state, adapter, source_id);
CREATE INDEX knowl_source_lifecycle_scan_eligibility ON knowl_source_lifecycle(scope, state, scan_complete, adapter, source_id);
