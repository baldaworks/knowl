package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// SQLite has no row version for a TEXT-primary-key table. Each rebuild assigns
// a new random rowid, which serves as a scope-local projection generation.
type projectionIdentity struct {
	exists bool
	rowID  int64
	digest string
}

func readProjectionIdentity(ctx context.Context, reader projectionReader, scope knowl.ScopeRef) (projectionIdentity, error) {
	var identity projectionIdentity
	err := reader.QueryRowContext(ctx, `SELECT rowid,snapshot_digest FROM knowl_projection_state WHERE scope=?`, scope).Scan(&identity.rowID, &identity.digest)
	if errors.Is(err, sql.ErrNoRows) {
		return identity, nil
	}
	if err != nil {
		return identity, err
	}
	identity.exists = true
	return identity, nil
}

func newProjectionIdentity(ctx context.Context, tx *sql.Tx, previous projectionIdentity) (int64, error) {
	for {
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return 0, err
		}
		nonce := int64(binary.BigEndian.Uint64(raw[:]) & (1<<63 - 1))
		if nonce == 0 || (previous.exists && nonce == previous.rowID) {
			continue
		}
		var occupied bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM knowl_projection_state WHERE rowid=?)`, nonce).Scan(&occupied); err != nil {
			return 0, err
		}
		if !occupied {
			return nonce, nil
		}
	}
}

func checkProjectionIdentity(ctx context.Context, tx *sql.Tx, scope knowl.ScopeRef, expected projectionIdentity) error {
	current, err := readProjectionIdentity(ctx, tx, scope)
	if err != nil {
		return err
	}
	if current != expected {
		return embeddingFailure(knowl.RetrievalProjectionDrift)
	}
	return nil
}
