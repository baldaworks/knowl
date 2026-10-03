package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type rebuildDeadlineProvider struct{ before func(context.Context) error }

func (p rebuildDeadlineProvider) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if p.before != nil {
		if err := p.before(ctx); err != nil {
			return nil, err
		}
	}
	vectors := make([][]float32, len(inputs))
	for i := range vectors {
		vectors[i] = []float32{1, 0}
	}
	return vectors, nil
}

func TestSQLiteRebuildDeadline(t *testing.T) {
	for _, phase := range []struct {
		name               string
		publication, mutex bool
	}{
		{"lexical-mutex", false, true}, {"lexical-sql", false, false}, {"publication-mutex", true, true}, {"publication-sql", true, false},
	} {
		t.Run(phase.name, func(t *testing.T) {
			var store *Store
			var conn *sql.Conn
			held := false
			heldSignal := make(chan struct{}, 1)
			holdOnce := false
			hold := func(ctx context.Context) error {
				if holdOnce {
					return nil
				}
				holdOnce = true
				defer func() { heldSignal <- struct{}{} }()
				if phase.mutex {
					store.mu.Lock()
					held = true
					return nil
				}
				var err error
				conn, err = store.db.Conn(ctx)
				return err
			}
			release := func() {
				if held {
					store.mu.Unlock()
					held = false
				}
				if conn != nil {
					_ = conn.Close()
					conn = nil
				}
			}
			p := rebuildDeadlineProvider{}
			if phase.publication {
				p.before = hold
			}
			var err error
			store, err = Open(t.Context(), t.TempDir()+"/deadline.sqlite", app.EmbeddingOptions{Provider: p, Space: app.EmbeddingSpace{Model: testFixture, Revision: "1", Dimensions: 2}})
			if err != nil {
				t.Fatal(err)
			}
			workerCtx, cancelWorker := context.WithCancel(t.Context())
			var finished chan struct{}
			t.Cleanup(func() {
				cancelWorker()
				release()
				if finished != nil {
					<-finished
				}
				_ = store.Close()
			})
			if !phase.publication {
				if err := hold(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			finished = make(chan struct{})
			go func() {
				defer close(finished)
				done <- store.rebuildWithTimeout(workerCtx, genericSQLiteSnapshot(), 200*time.Millisecond)
			}()
			select {
			case <-heldSignal:
			case err := <-done:
				t.Fatalf("rebuild exited before contention was established: %v", err)
			case <-time.After(2 * time.Second):
				t.Fatal("contention was not established")
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("rebuild error=%v; want deadline", err)
				}
			case <-time.After(2 * time.Second):
				release()
				<-done
				t.Fatal("rebuild did not stop while its SQL/lock resource remained held")
			}
			release()
			// Deadline expiry must not publish a ready or successful degraded state.
			var count int
			if err := store.db.QueryRowContext(t.Context(), "SELECT count(*) FROM knowl_embedding_state WHERE scope=?", genericSQLiteSnapshot().Scope).Scan(&count); err != nil || count != 0 {
				t.Fatalf("dense states=%d, err=%v", count, err)
			}
		})
	}
}

func TestSQLiteInferenceFreeProjectionCancellation(t *testing.T) {
	called := false
	p := rebuildDeadlineProvider{before: func(context.Context) error { called = true; return nil }}
	store, err := Open(t.Context(), t.TempDir()+"/historical.sqlite", app.EmbeddingOptions{Provider: p, Space: app.EmbeddingSpace{Model: testFixture, Revision: "1", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.mu.Lock()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- store.ProjectWithoutInference(ctx, knowl.ContentCommit{Snapshot: genericSQLiteSnapshot()})
	}()
	select {
	case err := <-done:
		store.mu.Unlock()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("historical projection error=%v; want deadline", err)
		}
	case <-time.After(2 * time.Second):
		store.mu.Unlock()
		<-done
		t.Fatal("historical projection ignored cancellation while write lock was held")
	}
	if called {
		t.Fatal("historical projection called embeddings")
	}
}
