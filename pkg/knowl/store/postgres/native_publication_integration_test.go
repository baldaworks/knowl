//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/hybrid"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/testcontainers/testcontainers-go"
	pgcontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	nativeProjectOperation = "project"
	nativeQuery            = "Alpha"
)

func TestPostgresNativePublication(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	container, err := pgcontainer.Run(ctx, "pgvector/pgvector:0.8.2-pg16@sha256:00ba258a66dac104fd5171074a0084462a64a1369d8513f3d0a634e2f24d15bc", pgcontainer.WithDatabase("publication_tests"), pgcontainer.WithUsername("knowl"), pgcontainer.WithPassword("knowl"), pgcontainer.BasicWaitStrategies(), pgcontainer.WithSQLDriver("pgx"))
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupContainer(t, container)
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("float32-roundtrip-and-authenticated-reuse", func(t *testing.T) {
		lexical := nativeOpen(t, dsn)
		nativeExec(t, lexical.db, `CREATE SCHEMA "vector extension"`)
		nativeExec(t, lexical.db, `CREATE EXTENSION vector SCHEMA "vector extension"`)
		provider := &nativePublicationProvider{vector: []float32{0.6000000238418579, 0.800000011920929}}
		options := nativeOptions()
		options.Provider = provider
		store := nativeOpen(t, dsn, options)
		snapshot := nativePublicationSnapshot()
		if err := store.Rebuild(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
		prior, err := store.previousProjection(ctx, snapshot.Scope)
		if err != nil {
			t.Fatal(err)
		}
		if len(prior.chunks) != 1 || !reflect.DeepEqual(prior.chunks[0].Vector, provider.vector) {
			t.Fatalf("roundtrip chunks=%v", prior.chunks)
		}
		if err := store.Project(ctx, knowl.ContentCommit{Snapshot: snapshot}); err != nil {
			t.Fatal(err)
		}
		if provider.calls != 1 {
			t.Fatalf("unchanged projection inferred %d times", provider.calls)
		}
		var format int
		if err := store.db.QueryRowContext(ctx, `SELECT native_format FROM knowl_embedding_state WHERE scope=$1`, snapshot.Scope).Scan(&format); err != nil || format != 1 {
			t.Fatalf("format=%d error=%v", format, err)
		}
	})
	for _, operation := range []string{"rebuild", nativeProjectOperation} {
		for _, replacement := range []string{"same_digest", "recreated", "coverage"} {
			t.Run(operation+"_rejects_"+replacement, func(t *testing.T) {
				databaseDSN := nativeDatabase(t, dsn, operation+"_"+replacement)
				lexical := nativeOpen(t, databaseDSN)
				nativeExec(t, lexical.db, `CREATE EXTENSION vector`)
				options := nativeOptions()
				provider := &nativePublicationProvider{vector: []float32{1, 0}}
				options.Provider = provider
				store := nativeOpen(t, databaseDSN, options)
				snapshot := nativePublicationSnapshot()
				if operation == nativeProjectOperation {
					if err := store.Rebuild(ctx, snapshot); err != nil {
						t.Fatal(err)
					}
					snapshot.Pages[0].Body += "\nChanged input"
					snapshot.Pages[0].Digest = "publication-a-2"
				}
				peerOptions := nativeOptions()
				peerOptions.Provider = &nativePublicationProvider{vector: []float32{0, 1}}
				peer := nativeOpen(t, databaseDSN, peerOptions)
				entered, release := make(chan struct{}), make(chan struct{})
				provider.before = func(ctx context.Context) error {
					close(entered)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				workerCtx, stop := context.WithTimeout(ctx, 10*time.Second)
				defer stop()
				done := make(chan error, 1)
				go func() {
					if operation == nativeProjectOperation {
						done <- store.Project(workerCtx, knowl.ContentCommit{Snapshot: snapshot})
					} else {
						done <- store.Rebuild(workerCtx, snapshot)
					}
				}()
				select {
				case <-entered:
				case <-workerCtx.Done():
					t.Fatal(workerCtx.Err())
				}
				if replacement == "recreated" {
					nativeExec(t, peer.db, `DELETE FROM knowl_projection_state WHERE scope='publication'`)
				}
				peerSnapshot := snapshot
				peerSnapshot.Pages = append([]knowl.PageSnapshot(nil), snapshot.Pages...)
				if replacement == "coverage" {
					peerSnapshot.Pages = append(peerSnapshot.Pages, knowl.PageSnapshot{ID: "b", Path: "wiki/b.md", Digest: "b-1", Title: "B", Body: "# Beta\nSecond section"})
				}
				if err := peer.Rebuild(workerCtx, peerSnapshot); err != nil {
					t.Fatal(err)
				}
				close(release)
				assertEmbeddingFailure(t, <-done, knowl.RetrievalProjectionDrift)
				prior, err := peer.previousProjection(ctx, snapshot.Scope)
				if err != nil {
					t.Fatal(err)
				}
				if len(prior.chunks) != len(peerSnapshot.Pages) {
					t.Fatalf("peer chunks=%d", len(prior.chunks))
				}
				for _, chunk := range prior.chunks {
					if !reflect.DeepEqual(chunk.Vector, []float32{0, 1}) {
						t.Fatalf("stale vector published: %v", chunk.Vector)
					}
				}
			})
		}
	}
	t.Run("maintenance-validation-and-legacy-rebuild", func(t *testing.T) { runNativeMaintenanceValidation(t, dsn) })
	t.Run("atomic-publication-and-interruption", func(t *testing.T) { runNativePublicationAtomicity(t, dsn) })
	t.Run("read-only-schema-checks", func(t *testing.T) { runNativePublicationSchemaChecks(t, dsn) })
	t.Run("runtime-capability-revocation", func(t *testing.T) { runNativePublicationRuntimeRole(t, dsn) })
	t.Run("query-fails-closed-until-sql-retrieval", func(t *testing.T) { runNativePublicationQueryGate(t, dsn) })
	t.Run("provider-and-deadline-safety", func(t *testing.T) {
		databaseDSN := nativePublicationDatabase(t, dsn, "failure")
		runPostgresProjectProviderFailure(t, databaseDSN)
		runPostgresRebuildDeadline(t, databaseDSN)
		runPostgresInferenceFreeProjectionCancellation(t, databaseDSN)
	})
}

func runNativePublicationQueryGate(t *testing.T, dsn string) {
	databaseDSN := nativePublicationDatabase(t, dsn, "query_gate")
	store := nativeOpen(t, databaseDSN, nativeOptions())
	snapshot := nativePublicationSnapshot()
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []app.EmbeddingFailurePolicy{app.EmbeddingStrict, app.EmbeddingFallbackLexical} {
		store.embedding.FailurePolicy = policy
		refs, report, err := store.SearchWithReport(t.Context(), snapshot.Scope, nativeQuery, knowl.ReadLimits{Pages: 1}, nil)
		ids, contextReport, _, contextErr := store.SelectContextWithDiagnostics(t.Context(), snapshot.Scope, knowl.SourceSummary{Title: nativeQuery}, knowl.ReadLimits{Pages: 1})
		if policy == app.EmbeddingStrict {
			assertEmbeddingFailure(t, err, knowl.RetrievalUnavailable)
			assertEmbeddingFailure(t, contextErr, knowl.RetrievalUnavailable)
		} else {
			if err != nil || len(refs) != 1 || refs[0].ID != "a" || report.Effective != knowl.RetrievalDegraded || report.Reason != knowl.RetrievalUnavailable {
				t.Fatalf("fallback search=%v report=%+v error=%v", refs, report, err)
			}
			if contextErr != nil || len(ids) != 1 || ids[0] != "a" || contextReport.Effective != knowl.RetrievalDegraded || contextReport.Reason != knowl.RetrievalUnavailable {
				t.Fatalf("fallback context=%v report=%+v error=%v", ids, contextReport, contextErr)
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, _, err = store.SearchWithReport(ctx, snapshot.Scope, nativeQuery, knowl.ReadLimits{}, nil)
		_, _, _, contextErr = store.SelectContextWithDiagnostics(ctx, snapshot.Scope, knowl.SourceSummary{Title: nativeQuery}, knowl.ReadLimits{})
		if !errors.Is(err, context.Canceled) || !errors.Is(contextErr, context.Canceled) {
			t.Fatalf("cancellation search=%v context=%v", err, contextErr)
		}
	}
}

func runNativePublicationRuntimeRole(t *testing.T, dsn string) {
	databaseDSN := nativePublicationDatabase(t, dsn, "publication_role")
	owner := nativeOpen(t, databaseDSN, nativeOptions())
	nativeExec(t, owner.db, `CREATE ROLE publication_runtime LOGIN PASSWORD 'fixture-runtime'`)
	nativeExec(t, owner.db, `GRANT USAGE ON SCHEMA public TO publication_runtime`)
	nativeExec(t, owner.db, `GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO publication_runtime`)
	runtimeURL, err := url.Parse(databaseDSN)
	if err != nil {
		t.Fatal(err)
	}
	runtimeURL.User = url.UserPassword("publication_runtime", "fixture-runtime")
	store := nativeOpen(t, runtimeURL.String(), nativeOptions())
	snapshot := nativePublicationSnapshot()
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	prior, err := store.previousProjection(t.Context(), snapshot.Scope)
	if err != nil {
		t.Fatal(err)
	}
	nativeExec(t, owner.db, `REVOKE EXECUTE ON FUNCTION vector_dims(vector) FROM PUBLIC`)
	assertEmbeddingFailure(t, store.publishEmbeddingsChecked(t.Context(), snapshot.Scope, *prior.state, prior.chunks, &prior.lexical), knowl.RetrievalUnavailable)
	nativeAssertCount(t, owner.db, `SELECT count(*) FROM knowl_embedding_chunks`, 1)
}

func nativePublicationDatabase(t *testing.T, dsn, name string) string {
	t.Helper()
	databaseDSN := nativeDatabase(t, dsn, name)
	store := nativeOpen(t, databaseDSN)
	nativeExec(t, store.db, `CREATE EXTENSION vector`)
	return databaseDSN
}

func runNativeMaintenanceValidation(t *testing.T, dsn string) {
	for _, corruption := range []struct{ name, query string }{
		{"hash", `UPDATE knowl_embedding_chunks SET content_hash=repeat('f',64)`},
		{"vector", `UPDATE knowl_embedding_chunks SET vector='[0,0]'`},
		{"legacy", `UPDATE knowl_embedding_state SET native_format=0`},
	} {
		t.Run(corruption.name, func(t *testing.T) {
			databaseDSN := nativePublicationDatabase(t, dsn, "reuse_"+corruption.name)
			provider := &nativePublicationProvider{vector: []float32{1, 0}}
			options := nativeOptions()
			options.Provider = provider
			store := nativeOpen(t, databaseDSN, options)
			snapshot := nativePublicationSnapshot()
			if err := store.Rebuild(t.Context(), snapshot); err != nil {
				t.Fatal(err)
			}
			nativeExec(t, store.db, corruption.query)
			if err := store.Project(t.Context(), knowl.ContentCommit{Snapshot: snapshot}); err != nil {
				t.Fatal(err)
			}
			if provider.calls != 2 {
				t.Fatalf("corrupt/legacy projection reused: provider calls=%d", provider.calls)
			}
			prior, err := store.previousProjection(t.Context(), snapshot.Scope)
			if err != nil || prior.state == nil || len(prior.chunks) != 1 {
				t.Fatalf("rebuilt projection=%+v error=%v", prior, err)
			}
		})
	}
	for _, dimensions := range []int{1, 2000, 2001, 4096} {
		t.Run(fmt.Sprint(dimensions), func(t *testing.T) {
			databaseDSN := nativePublicationDatabase(t, dsn, fmt.Sprintf("dimension_%d", dimensions))
			vector := make([]float32, dimensions)
			vector[0] = 1
			if dimensions > 1 {
				vector[0], vector[dimensions-1] = 0.6000000238418579, 0.800000011920929
			}
			options := nativeOptions()
			options.Space.Dimensions = dimensions
			options.Provider = &nativePublicationProvider{vector: vector}
			store := nativeOpen(t, databaseDSN, options)
			snapshot := nativePublicationSnapshot()
			if err := store.Rebuild(t.Context(), snapshot); err != nil {
				t.Fatal(err)
			}
			prior, err := store.previousProjection(t.Context(), snapshot.Scope)
			if err != nil || len(prior.chunks) != 1 || !reflect.DeepEqual(prior.chunks[0].Vector, vector) {
				t.Fatalf("dimension %d roundtrip error=%v", dimensions, err)
			}
		})
	}
}

func runNativePublicationAtomicity(t *testing.T, dsn string) {
	databaseDSN := nativePublicationDatabase(t, dsn, "atomic_publication")
	store := nativeOpen(t, databaseDSN, nativeOptions())
	peer := nativeOpen(t, databaseDSN, nativeOptions())
	snapshot := nativePublicationSnapshot()
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	prior, err := store.previousProjection(t.Context(), snapshot.Scope)
	if err != nil {
		t.Fatal(err)
	}
	staged := append([]hybrid.Chunk(nil), prior.chunks...)
	staged[0].Vector = []float32{0, 1}
	// Hold insertion after readiness has been replaced inside the writer tx.
	nativeExec(t, peer.db, `CREATE FUNCTION gate_chunk_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(928371); RETURN NEW; END $$`)
	nativeExec(t, peer.db, `CREATE TRIGGER gate_chunk_insert BEFORE INSERT ON knowl_embedding_chunks FOR EACH ROW EXECUTE FUNCTION gate_chunk_insert()`)
	for _, interrupt := range []bool{true, false} {
		tx, err := peer.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(t.Context(), `SELECT pg_advisory_xact_lock(928371)`); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		done := make(chan error, 1)
		go func() {
			done <- store.publishEmbeddingsChecked(ctx, snapshot.Scope, *prior.state, staged, &prior.lexical)
		}()
		waitForProjectionLock(t, ctx, peer, done)
		visible, err := peer.previousProjection(t.Context(), snapshot.Scope)
		if err != nil || len(visible.chunks) != 1 || !reflect.DeepEqual(visible.chunks[0].Vector, []float32{1, 0}) {
			t.Fatalf("partial publication visible=%+v error=%v", visible, err)
		}
		if interrupt {
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("interrupted publication error=%v", err)
			}
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if !interrupt {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		cancel()
		visible, err = peer.previousProjection(t.Context(), snapshot.Scope)
		want := []float32{1, 0}
		if !interrupt {
			want = []float32{0, 1}
		}
		if err != nil || len(visible.chunks) != 1 || !reflect.DeepEqual(visible.chunks[0].Vector, want) {
			t.Fatalf("committed projection=%+v error=%v", visible, err)
		}
	}
}

func runNativePublicationSchemaChecks(t *testing.T, dsn string) {
	for _, corruption := range []struct{ name, query string }{
		{"history", `DELETE FROM goose_pgvector_db_version`},
		{"shape", `ALTER TABLE knowl_embedding_chunks DROP CONSTRAINT knowl_embedding_chunks_vector_check`},
	} {
		t.Run(corruption.name, func(t *testing.T) {
			databaseDSN := nativePublicationDatabase(t, dsn, "schema_"+corruption.name)
			store := nativeOpen(t, databaseDSN, nativeOptions())
			snapshot := nativePublicationSnapshot()
			if err := store.Rebuild(t.Context(), snapshot); err != nil {
				t.Fatal(err)
			}
			prior, err := store.previousProjection(t.Context(), snapshot.Scope)
			if err != nil {
				t.Fatal(err)
			}
			nativeExec(t, store.db, corruption.query)
			assertEmbeddingFailure(t, store.publishEmbeddingsChecked(t.Context(), snapshot.Scope, *prior.state, prior.chunks, &prior.lexical), knowl.RetrievalUnavailable)
			_, err = store.previousProjection(t.Context(), snapshot.Scope)
			assertEmbeddingFailure(t, err, knowl.RetrievalUnavailable)
			nativeAssertCount(t, store.db, `SELECT count(*) FROM knowl_embedding_chunks`, 1)
			if corruption.name == "history" {
				nativeAssertCount(t, store.db, `SELECT count(*) FROM goose_pgvector_db_version`, 0)
			} else {
				nativeAssertCount(t, store.db, `SELECT count(*) FROM pg_constraint WHERE conrelid='knowl_embedding_chunks'::regclass AND conname='knowl_embedding_chunks_vector_check'`, 0)
			}
		})
	}
}

type nativePublicationProvider struct {
	vector []float32
	calls  int
	before func(context.Context) error
}

func (provider *nativePublicationProvider) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	provider.calls++
	if provider.before != nil {
		if err := provider.before(ctx); err != nil {
			return nil, err
		}
	}
	values := make([][]float32, len(inputs))
	for i := range values {
		values[i] = append([]float32(nil), provider.vector...)
	}
	return values, nil
}

func nativePublicationSnapshot() knowl.WorkspaceSnapshot {
	return knowl.WorkspaceSnapshot{Scope: "publication", SchemaDigest: "schema", Pages: []knowl.PageSnapshot{{ID: "a", Path: "wiki/publication.md", Digest: "publication-a-1", Title: "A", Body: "# Alpha\nFirst section"}}}
}

var _ app.EmbeddingProvider = (*nativePublicationProvider)(nil)
