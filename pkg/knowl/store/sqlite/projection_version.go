package sqlite

import (
	"context"
	"database/sql"
	"net/url"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// projectionVersion observes commits made by other SQLite connections. It is
// separate from Store.db so inference never holds the store's only connection.
type projectionVersion struct {
	db   *sql.DB
	conn *sql.Conn
}

func openProjectionVersion(ctx context.Context, path string) (*projectionVersion, error) {
	databaseURL := url.URL{Scheme: "file", Path: path}
	query := databaseURL.Query()
	query.Set("mode", "ro")
	databaseURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &projectionVersion{db: db, conn: conn}, nil
}

func (version *projectionVersion) close() {
	_ = version.conn.Close()
	_ = version.db.Close()
}

func (version *projectionVersion) current(ctx context.Context) (int64, error) {
	var current int64
	err := version.conn.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&current)
	return current, err
}

func (version *projectionVersion) check(ctx context.Context, observed int64) error {
	current, err := version.current(ctx)
	if err != nil {
		return err
	}
	if current != observed {
		return embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	return nil
}
