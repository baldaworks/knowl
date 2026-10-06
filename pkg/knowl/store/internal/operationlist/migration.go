// Package operationlist shares validated operation-list migration rules.
package operationlist

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const maxSourceDocumentBytes = 64 << 10

// SourceID returns a nullable configured owner from complete bounded provenance.
func SourceID(raw string) any {
	if len(raw) == 0 || len(raw) > maxSourceDocumentBytes {
		return nil
	}
	var document knowl.SourceDocument
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || decoder.Decode(new(any)) != io.EOF || app.ValidateSourceDocument(document) != nil {
		return nil
	}
	return string(document.SourceID)
}

// SortTime preserves nanosecond precision while making UTC timestamps sortable.
func SortTime(at time.Time) string { return at.UTC().Format("2006-01-02T15:04:05.000000000Z") }

// Backfill populates only new selectors in bounded batches without altering history.
// Malformed source metadata leaves its selector NULL. Invalid SQLite creation
// timestamps abort the transactional migration, retaining all original rows.
func Backfill(ctx context.Context, tx *sql.Tx, sqlite bool) error {
	after := ""
	for {
		query := `SELECT operation_id, CASE WHEN octet_length(accepted_source_document)<=65536 THEN accepted_source_document ELSE '' END FROM knowl_operations WHERE operation_id>$1 ORDER BY operation_id LIMIT 100`
		if sqlite {
			query = `SELECT operation_id, CASE WHEN length(CAST(accepted_source_document AS BLOB))<=65536 THEN accepted_source_document ELSE '' END, created_at FROM knowl_operations WHERE operation_id>? ORDER BY operation_id LIMIT 100`
		}
		rows, err := tx.QueryContext(ctx, query, after)
		if err != nil {
			return err
		}
		type row struct{ id, document, created string }
		batch := make([]row, 0, 100)
		for rows.Next() {
			var value row
			var err error
			if sqlite {
				err = rows.Scan(&value.id, &value.document, &value.created)
			} else {
				err = rows.Scan(&value.id, &value.document)
			}
			if err != nil {
				_ = rows.Close()
				return err
			}
			batch = append(batch, value)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		for _, value := range batch {
			if sqlite {
				at, err := time.Parse(time.RFC3339Nano, value.created)
				if err != nil {
					return fmt.Errorf("parse operation creation time: %w", err)
				}
				_, err = tx.ExecContext(ctx, `UPDATE knowl_operations SET configured_source_id=?, created_at_sort=? WHERE operation_id=?`, SourceID(value.document), SortTime(at), value.id)
				if err != nil {
					return err
				}
			} else {
				if _, err := tx.ExecContext(ctx, `UPDATE knowl_operations SET configured_source_id=$1 WHERE operation_id=$2`, SourceID(value.document), value.id); err != nil {
					return err
				}
			}
			after = value.id
		}
		if len(batch) < 100 {
			return nil
		}
	}
}
