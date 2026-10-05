-- +goose Up
ALTER TABLE knowl_operations ADD COLUMN context_report TEXT;
ALTER TABLE knowl_operations ADD COLUMN plan_file_count INTEGER CHECK (plan_file_count >= 0 AND plan_file_count <= 2147483647);

-- +goose Down
-- Discards only the new operational report/count; canonical content is unaffected.
ALTER TABLE knowl_operations DROP COLUMN plan_file_count;
ALTER TABLE knowl_operations DROP COLUMN context_report;
