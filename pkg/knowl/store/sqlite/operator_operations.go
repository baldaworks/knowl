package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/operationlist"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

var _ app.OperationLister = (*Store)(nil)

// ListOperations reads bounded summaries in immutable creation order.
func (store *Store) ListOperations(ctx context.Context, scope knowl.ScopeRef, options app.OperatorOperationReadOptions) (app.OperatorReadPage[knowl.OperatorOperationSummary], error) {
	result := app.OperatorReadPage[knowl.OperatorOperationSummary]{Items: make([]knowl.OperatorOperationSummary, 0)}
	if options.Limit < 1 || options.Limit > 100 {
		return result, app.ErrOperatorLimitInvalid
	}
	query := `SELECT operation_id, work_kind, status, configured_source_id, created_at, updated_at FROM knowl_operations WHERE scope=?1`
	args := []any{scope}
	placeholder := func() string { return "?" + strconv.Itoa(len(args)) }
	if options.Status != "" {
		args = append(args, options.Status)
		query += " AND status=" + placeholder()
	}
	if options.SourceID != "" {
		args = append(args, options.SourceID)
		query += " AND configured_source_id=" + placeholder()
	}
	if options.Continuation.Key != "" {
		position, err := app.DecodeOperatorOperationPosition(options.Continuation.Key)
		if err != nil {
			return result, err
		}
		args = append(args, operationlist.SortTime(position.CreatedAt))
		at := placeholder()
		args = append(args, position.OperationID)
		id := placeholder()
		query += " AND (created_at_sort, operation_id) < (" + at + ", " + id + ")"
	}
	args = append(args, options.Limit+1)
	query += " ORDER BY created_at_sort DESC, operation_id DESC LIMIT " + placeholder()
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return result, fmt.Errorf("list operations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var item knowl.OperatorOperationSummary
		var source sql.NullString
		var created, updated string
		if err := rows.Scan(&item.ID, &item.Kind, &item.Status, &source, &created, &updated); err != nil {
			return result, fmt.Errorf("scan operation summary: %w", err)
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return result, fmt.Errorf("parse operation creation time: %w", err)
		}
		item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return result, fmt.Errorf("parse operation update time: %w", err)
		}
		item.SourceID = knowl.SourceID(source.String)
		item.CreatedAt = item.CreatedAt.UTC()
		item.UpdatedAt = item.UpdatedAt.UTC()
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("list operation rows: %w", err)
	}
	if len(result.Items) > options.Limit {
		result.Items = result.Items[:options.Limit]
		last := result.Items[len(result.Items)-1]
		result.NextKey, err = app.EncodeOperatorOperationPosition(app.OperatorOperationPosition{CreatedAt: last.CreatedAt, OperationID: last.ID})
		if err != nil {
			return result, err
		}
	}
	return result, nil
}
