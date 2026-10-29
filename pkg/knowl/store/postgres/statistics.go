package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

var postgresStatisticsTables = [...]struct {
	name  string
	table string
}{
	{knowl.StatisticsTableOperations, "knowl_" + knowl.StatisticsTableOperations},
	{knowl.StatisticsTablePages, "knowl_" + knowl.StatisticsTablePages},
	{knowl.StatisticsTableLinks, "knowl_" + knowl.StatisticsTableLinks},
	{knowl.StatisticsTableProjectionState, "knowl_" + knowl.StatisticsTableProjectionState},
	{knowl.StatisticsTableSources, "knowl_" + knowl.StatisticsTableSources},
	{knowl.StatisticsTableSourceDocuments, "knowl_" + knowl.StatisticsTableSourceDocuments},
	{knowl.StatisticsTablePageSources, "knowl_" + knowl.StatisticsTablePageSources},
	{knowl.StatisticsTableEmbeddingState, "knowl_" + knowl.StatisticsTableEmbeddingState},
	{knowl.StatisticsTableEmbeddingChunks, "knowl_" + knowl.StatisticsTableEmbeddingChunks},
}

// Statistics reads one repeatable, read-only current snapshot and never
// persists the observation.
func (store *Store) Statistics(ctx context.Context, scope knowl.ScopeRef) (result knowl.OperatorStatistics, err error) {
	if err := validateScope(scope); err != nil {
		return result, err
	}
	observedAt := time.Now().UTC()
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return result, fmt.Errorf("begin statistics snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result.ObservedAt = observedAt
	result.Storage.Backend = knowl.StatisticsBackendPostgres
	result.Product.OperationsByStatus = emptyStatisticsStatuses()
	if err := readPostgresProductStatistics(ctx, tx, scope, &result); err != nil {
		return knowl.OperatorStatistics{}, err
	}
	readPostgresProjectionStatistics(ctx, tx, store.embedding, scope, &result)
	if err := readPostgresStorageStatistics(ctx, tx, &result.Storage); err != nil {
		return knowl.OperatorStatistics{}, err
	}
	if err := tx.Commit(); err != nil {
		return knowl.OperatorStatistics{}, fmt.Errorf("commit statistics snapshot: %w", err)
	}
	return result, nil
}

func readPostgresProductStatistics(ctx context.Context, tx *sql.Tx, scope knowl.ScopeRef, result *knowl.OperatorStatistics) error {
	var pages, links int64
	err := tx.QueryRowContext(ctx, `SELECT page_count,link_count FROM knowl_projection_state WHERE scope=$1`, scope).Scan(&pages, &links)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		result.Projection.LexicalStatus = knowl.ProjectionStatisticsUnavailable
	case err != nil:
		return fmt.Errorf("read statistics projection counts: %w", err)
	case pages < 0 || links < 0:
		result.Projection.LexicalStatus = knowl.ProjectionStatisticsInvalid
	default:
		result.Product.Pages, result.Product.Links = pages, links
		result.Projection.EligiblePages = pages
		result.Projection.LexicalStatus = knowl.ProjectionStatisticsReady
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowl_source_documents WHERE scope=$1`, scope).Scan(&result.Product.SourceDocuments); err != nil {
		return fmt.Errorf("read statistics source documents: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT status,COUNT(*) FROM knowl_operations WHERE scope=$1 GROUP BY status`, scope)
	if err != nil {
		return fmt.Errorf("read statistics operations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var status knowl.OperationStatus
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return fmt.Errorf("scan statistics operations: %w", err)
		}
		if _, known := result.Product.OperationsByStatus[status]; !known || count < 0 || count > math.MaxInt64-result.Product.OperationsTotal {
			return errors.New("invalid statistics operation aggregate")
		}
		result.Product.OperationsByStatus[status] = count
		result.Product.OperationsTotal += count
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate statistics operations: %w", err)
	}
	return nil
}

func readPostgresProjectionStatistics(ctx context.Context, tx *sql.Tx, engine *hybrid.Engine, scope knowl.ScopeRef, result *knowl.OperatorStatistics) {
	if engine == nil {
		result.Projection.EmbeddingStatus = knowl.ProjectionStatisticsDisabled
		return
	}
	if result.Projection.LexicalStatus != knowl.ProjectionStatisticsReady {
		result.Projection.EmbeddingStatus = knowl.ProjectionStatisticsUnavailable
		return
	}
	var state hybrid.ProjectionState
	var lexicalSnapshot string
	var nativeFormat int
	err := tx.QueryRowContext(ctx, `
		SELECT e.space,e.snapshot_digest,e.dimensions,e.mode,e.reason,e.chunk_count,
		       e.omitted_chunks,e.omitted_runes,e.coverage,p.snapshot_digest,e.native_format
		FROM knowl_embedding_state e JOIN knowl_projection_state p ON p.scope=e.scope
		WHERE e.scope=$1`, scope).Scan(&state.Space, &state.SnapshotDigest, &state.Dimensions, &state.Mode, &state.Reason, &state.ChunkCount, &state.OmittedChunks, &state.OmittedRunes, &state.Coverage, &lexicalSnapshot, &nativeFormat)
	if errors.Is(err, sql.ErrNoRows) {
		result.Projection.EmbeddingStatus = knowl.ProjectionStatisticsUnavailable
		return
	}
	if err != nil || state.SnapshotDigest != lexicalSnapshot || state.Space != engine.Fingerprint || state.Dimensions != engine.Space.Dimensions || nativeFormat != 1 {
		result.Projection.EmbeddingStatus = knowl.ProjectionStatisticsInvalid
		return
	}
	coverage, err := hybrid.ValidateProjectionMetadata(ctx, state)
	if err != nil {
		result.Projection.EmbeddingStatus = knowl.ProjectionStatisticsInvalid
		return
	}
	if state.Mode == knowl.RetrievalDegraded {
		result.Projection.EmbeddingStatus = knowl.ProjectionStatisticsIncomplete
		return
	}
	pages, err := embeddingPageSetTx(ctx, tx, scope)
	if err != nil || hybrid.ValidatePageSet(state.Coverage, pages) != nil {
		result.Projection.EmbeddingStatus = knowl.ProjectionStatisticsInvalid
		return
	}
	var chunks int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowl_embedding_chunks WHERE scope=$1`, scope).Scan(&chunks); err != nil || chunks != int64(state.ChunkCount) {
		result.Projection.EmbeddingStatus = knowl.ProjectionStatisticsInvalid
		return
	}
	for _, page := range coverage {
		if page.Chunks > 0 {
			result.Projection.EmbeddedPages++
		}
	}
	result.Projection.Chunks = chunks
	result.Projection.EmbeddingStatus = knowl.ProjectionStatisticsReady
}

func readPostgresStorageStatistics(ctx context.Context, tx *sql.Tx, storage *knowl.StorageStatistics) error {
	metric, err := postgresOptionalMetric(ctx, tx, `SELECT pg_database_size(current_database())`)
	if err != nil {
		return err
	}
	storage.DatabaseBytes = metric
	storage.Tables = make([]knowl.TableStatistics, 0, len(postgresStatisticsTables))
	for _, definition := range postgresStatisticsTables {
		rows, err := postgresStatisticsRowCount(ctx, tx, definition.table)
		if err != nil {
			return fmt.Errorf("read statistics table %s: %w", definition.name, err)
		}
		table := knowl.TableStatistics{
			Name: definition.name, Rows: rows,
			DataBytes:  unavailableStatisticsMetric(knowl.StatisticsTemporarilyUnavailable),
			IndexBytes: unavailableStatisticsMetric(knowl.StatisticsTemporarilyUnavailable),
			TotalBytes: unavailableStatisticsMetric(knowl.StatisticsTemporarilyUnavailable),
		}
		values, availability, err := postgresOptionalTableSizes(ctx, tx, definition.table)
		if err != nil {
			return err
		}
		if availability == knowl.StatisticsAvailable {
			table.DataBytes = availableStatisticsMetric(values[0])
			table.IndexBytes = availableStatisticsMetric(values[1])
			table.TotalBytes = availableStatisticsMetric(values[2])
		} else {
			table.DataBytes = unavailableStatisticsMetric(availability)
			table.IndexBytes = unavailableStatisticsMetric(availability)
			table.TotalBytes = unavailableStatisticsMetric(availability)
		}
		storage.Tables = append(storage.Tables, table)
	}
	return nil
}

func postgresOptionalMetric(ctx context.Context, tx *sql.Tx, query string) (knowl.StatisticsMetric, error) {
	if _, err := tx.ExecContext(ctx, `SAVEPOINT knowl_statistics_optional`); err != nil {
		return knowl.StatisticsMetric{}, err
	}
	var value int64
	err := tx.QueryRowContext(ctx, query).Scan(&value)
	if err == nil && value >= 0 {
		if _, releaseErr := tx.ExecContext(ctx, `RELEASE SAVEPOINT knowl_statistics_optional`); releaseErr != nil {
			return knowl.StatisticsMetric{}, releaseErr
		}
		return availableStatisticsMetric(value), nil
	}
	if ctx.Err() != nil {
		return knowl.StatisticsMetric{}, ctx.Err()
	}
	if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT knowl_statistics_optional`); rollbackErr != nil {
		return knowl.StatisticsMetric{}, rollbackErr
	}
	if _, releaseErr := tx.ExecContext(ctx, `RELEASE SAVEPOINT knowl_statistics_optional`); releaseErr != nil {
		return knowl.StatisticsMetric{}, releaseErr
	}
	return unavailableStatisticsMetric(postgresStatisticsAvailability(err)), nil
}

func postgresOptionalTableSizes(ctx context.Context, tx *sql.Tx, table string) ([3]int64, knowl.StatisticsAvailability, error) {
	var values [3]int64
	if _, err := tx.ExecContext(ctx, `SAVEPOINT knowl_statistics_optional`); err != nil {
		return values, "", err
	}
	err := tx.QueryRowContext(ctx, `SELECT pg_relation_size(to_regclass($1)),pg_indexes_size(to_regclass($1)),pg_total_relation_size(to_regclass($1))`, table).Scan(&values[0], &values[1], &values[2])
	if err == nil && values[0] >= 0 && values[1] >= 0 && values[2] >= 0 {
		if _, releaseErr := tx.ExecContext(ctx, `RELEASE SAVEPOINT knowl_statistics_optional`); releaseErr != nil {
			return values, "", releaseErr
		}
		return values, knowl.StatisticsAvailable, nil
	}
	if ctx.Err() != nil {
		return values, "", ctx.Err()
	}
	if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT knowl_statistics_optional`); rollbackErr != nil {
		return values, "", rollbackErr
	}
	if _, releaseErr := tx.ExecContext(ctx, `RELEASE SAVEPOINT knowl_statistics_optional`); releaseErr != nil {
		return values, "", releaseErr
	}
	return values, postgresStatisticsAvailability(err), nil
}

func postgresStatisticsAvailability(err error) knowl.StatisticsAvailability {
	var state interface{ SQLState() string }
	if errors.As(err, &state) && state.SQLState() == "42501" {
		return knowl.StatisticsPermissionDenied
	}
	return knowl.StatisticsTemporarilyUnavailable
}

func postgresStatisticsRowCount(ctx context.Context, tx *sql.Tx, table string) (int64, error) {
	queries := map[string]string{
		"knowl_operations":       `SELECT COUNT(*) FROM knowl_operations`,
		"knowl_pages":            `SELECT COUNT(*) FROM knowl_pages`,
		"knowl_links":            `SELECT COUNT(*) FROM knowl_links`,
		"knowl_projection_state": `SELECT COUNT(*) FROM knowl_projection_state`,
		"knowl_sources":          `SELECT COUNT(*) FROM knowl_sources`,
		"knowl_source_documents": `SELECT COUNT(*) FROM knowl_source_documents`,
		"knowl_page_sources":     `SELECT COUNT(*) FROM knowl_page_sources`,
		"knowl_embedding_state":  `SELECT COUNT(*) FROM knowl_embedding_state`,
		"knowl_embedding_chunks": `SELECT COUNT(*) FROM knowl_embedding_chunks`,
	}
	query, ok := queries[table]
	if !ok {
		return 0, errors.New("statistics table is not allowlisted")
	}
	var count int64
	if err := tx.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return 0, err
	}
	if count < 0 {
		return 0, errors.New("invalid statistics row count")
	}
	return count, nil
}

func emptyStatisticsStatuses() map[knowl.OperationStatus]int64 {
	return map[knowl.OperationStatus]int64{
		knowl.StatusReceived: 0, knowl.StatusPlanned: 0, knowl.StatusAwaitingReview: 0,
		knowl.StatusApplying: 0, knowl.StatusCommitted: 0, knowl.StatusFailed: 0,
	}
}

func availableStatisticsMetric(value int64) knowl.StatisticsMetric {
	return knowl.StatisticsMetric{Availability: knowl.StatisticsAvailable, Value: value}
}

func unavailableStatisticsMetric(reason knowl.StatisticsAvailability) knowl.StatisticsMetric {
	return knowl.StatisticsMetric{Availability: reason}
}
