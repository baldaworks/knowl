-- +goose Up
CREATE TABLE knowl_source_lifecycle_authority (
    scope TEXT PRIMARY KEY,
    generation TEXT NOT NULL DEFAULT '' CHECK (generation = '' OR length(generation) = 64),
    epoch INTEGER NOT NULL DEFAULT 0 CHECK (epoch >= 0),
    updated_at TEXT NOT NULL
);

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
    PRIMARY KEY(scope, adapter, source_id)
);

INSERT INTO knowl_source_lifecycle_authority (scope, updated_at)
SELECT scope, MAX(updated_at) FROM knowl_sources GROUP BY scope;

INSERT INTO knowl_source_lifecycle (
    scope, adapter, source_id, state, config_digest, authority_generation,
    activated_generation, activated_at, updated_at
)
SELECT scope,
       CASE source_type WHEN 'filesystem' THEN 'wiki-filesystem' WHEN 'git' THEN 'git' END,
       source_id, 'active', config_digest, config_digest, config_digest, created_at, updated_at
FROM knowl_sources
WHERE source_type IN ('filesystem', 'git');

CREATE INDEX knowl_source_lifecycle_scope_state
    ON knowl_source_lifecycle(scope, state, adapter, source_id);
CREATE INDEX knowl_source_lifecycle_scan_eligibility
    ON knowl_source_lifecycle(scope, state, scan_complete, adapter, source_id);

-- +goose Down
DROP INDEX knowl_source_lifecycle_scan_eligibility;
DROP INDEX knowl_source_lifecycle_scope_state;
DROP TABLE knowl_source_lifecycle;
DROP TABLE knowl_source_lifecycle_authority;
