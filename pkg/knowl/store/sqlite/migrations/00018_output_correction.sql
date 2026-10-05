-- +goose Up
ALTER TABLE knowl_operations ADD COLUMN correction_report TEXT;

-- +goose Down
ALTER TABLE knowl_operations DROP COLUMN correction_report;
