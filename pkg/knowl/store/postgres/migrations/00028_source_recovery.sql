-- +goose Up
ALTER TABLE knowl_source_lifecycle ADD COLUMN recovery_proof_payload TEXT NOT NULL DEFAULT '' CHECK (octet_length(recovery_proof_payload) <= 65536);
ALTER TABLE knowl_source_lifecycle ADD COLUMN recovery_proof_digest TEXT NOT NULL DEFAULT '' CHECK (recovery_proof_digest = '' OR length(recovery_proof_digest) = 64);
ALTER TABLE knowl_source_lifecycle DROP CONSTRAINT knowl_source_lifecycle_config_digest_check;
ALTER TABLE knowl_source_lifecycle ADD CONSTRAINT knowl_source_lifecycle_binding_check CHECK (
 (length(config_digest) = 64 AND recovery_proof_digest = '') OR
 (config_digest = '' AND length(recovery_proof_digest) = 64 AND state IN ('retiring', 'deleted') AND recovery_proof_payload <> '')
);

-- +goose Down
ALTER TABLE knowl_source_lifecycle ADD CONSTRAINT knowl_source_recovery_down_guard CHECK (recovery_proof_payload = '');
-- Restoring the old constraint rejects recovery-only rows without erasing proof.
ALTER TABLE knowl_source_lifecycle ADD CONSTRAINT knowl_source_lifecycle_config_digest_check CHECK (length(config_digest) = 64);
ALTER TABLE knowl_source_lifecycle DROP CONSTRAINT knowl_source_lifecycle_binding_check;
ALTER TABLE knowl_source_lifecycle DROP COLUMN recovery_proof_digest;
ALTER TABLE knowl_source_lifecycle DROP CONSTRAINT knowl_source_recovery_down_guard;
ALTER TABLE knowl_source_lifecycle DROP COLUMN recovery_proof_payload;
