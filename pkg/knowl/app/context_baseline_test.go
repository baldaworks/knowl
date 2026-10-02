package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/internal/knowledgetest"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// This provider substitutes inference only; context is chosen by the real app/index.
const baselineMet = "met"

type baselineMaintainer struct{ input knowl.MaintenanceInput }

func (m *baselineMaintainer) Plan(_ context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	m.input = input
	return knowl.ModelEditPlan{SchemaDigest: input.Schema.Digest, SourceRefs: []string{app.SourceRefKey(input.Source)}}, nil
}

func TestContextBaselineSourceSignals(t *testing.T) {
	for _, fixture := range knowledgetest.BaselineSources() {
		t.Run(fixture.ID, func(t *testing.T) {
			var previous knowledgetest.RecallObservation
			for pass := range 2 {
				workspace, store, maintainer, service := newBaselineIngest(t)
				seedBaselineContext(t, workspace, store)
				before, err := workspace.Snapshot(t.Context(), "local")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := service.Ingest(t.Context(), sourceEnvelope([]byte(fixture.Content))); err != nil {
					t.Fatalf("real ingest: %v", err)
				}
				if maintainer.input.SourceText != fixture.Content {
					t.Fatal("ingest did not preserve authoritative source text")
				}
				ids := make([]knowl.PageID, 0, len(maintainer.input.Pages))
				for _, p := range maintainer.input.Pages {
					ids = append(ids, p.ID)
				}
				result := knowledgetest.ObserveRecall(fixture.ID, []knowl.PageID{fixture.ExpectedPage}, ids, 20)
				logBaseline(t, result)
				if pass == 1 && !reflect.DeepEqual(previous, result) {
					t.Fatal("source context changed between fresh fixed-input workspaces")
				}
				previous = result
				after, err := workspace.Snapshot(t.Context(), "local")
				if err != nil || !reflect.DeepEqual(before.PageDigests, after.PageDigests) {
					t.Fatalf("no-op measurement changed canonical pages: %v", err)
				}
				inspection, err := workspace.Inspect(t.Context(), "local")
				if err != nil {
					t.Fatal(err)
				}
				for _, raw := range inspection.RawSources {
					if !raw.Valid {
						t.Fatal("baseline invalidated immutable raw")
					}
				}
			}
		})
	}
}

func TestContextBaselineCatalogScaling(t *testing.T) {
	workspace, _, maintainer, service := newBaselineIngest(t)
	root, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki/index.md"))
	if err != nil {
		t.Fatal(err)
	}
	// Root plus 31 directories: catalog count exceeds the default page limit of 20.
	for i := range 31 {
		name := fmt.Sprintf("catalog-%02d", i)
		writeBaselineFile(t, workspace.Root(), "wiki/"+name+"/index.md", "# Catalog\n", time.Date(2026, 8, 14, 12, 0, i, 0, time.UTC))
		root = append(root, []byte(fmt.Sprintf("\n* [%s](%s/index.md)\n", name, name))...)
	}
	writeBaselineFile(t, workspace.Root(), "wiki/index.md", string(root), time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC))
	before, err := workspace.Snapshot(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Ingest(t.Context(), sourceEnvelope([]byte("# Catalog maintenance")))
	outcome := baselineMet
	if errors.Is(err, app.ErrPlanLimitExceeded) {
		outcome = "gap"
	} else if err != nil {
		t.Fatalf("unexpected catalog failure: %v", err)
	}
	if outcome == baselineMet && len(maintainer.input.Catalogs) != 32 {
		t.Fatal("successful ingest omitted catalogs")
	}
	logBaseline(t, struct {
		CaseID   string `json:"case_id"`
		Catalogs int    `json:"catalogs"`
		Pages    int    `json:"page_limit"`
		Outcome  string `json:"outcome"`
	}{"catalog-scaling", 32, 20, outcome})
	after, snapshotErr := workspace.Snapshot(t.Context(), "local")
	if snapshotErr != nil || !reflect.DeepEqual(before.PageDigests, after.PageDigests) {
		t.Fatalf("catalog measurement mutated canonical data: %v", snapshotErr)
	}
}

func newBaselineIngest(t *testing.T) (*contentfs.Workspace, *sqlite.Store, *baselineMaintainer, *app.IngestService) {
	t.Helper()
	fixed := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	workspace, err := contentfs.New(t.TempDir(), contentfs.WithClock(func() time.Time { return fixed }))
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(t.Context(), filepath.Join(workspace.Root(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	maintainer := &baselineMaintainer{}
	service, err := app.NewIngestService(workspace, store, store, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return workspace, store, maintainer, service
}

func seedBaselineContext(t *testing.T, workspace *contentfs.Workspace, store *sqlite.Store) {
	t.Helper()
	fixed := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	// A real accepted source justifies the canonical page's provenance.
	seed := sourceEnvelope([]byte("quasarretention evidence"))
	seed.Source.ID = "baseline-seed"
	if _, err := workspace.AcceptSource(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	const seedRef = "fixture:baseline-seed@1"
	content := "---\nid: decisions/storage\ntitle: Quasarretention\ntype: decision\nsource_refs: [" + seedRef + "]\n---\n# Quasarretention\n\nStorage requirements.\n"
	writeBaselineFile(t, workspace.Root(), "wiki/decisions/storage.md", content, fixed.Add(-48*time.Hour))
	root, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki/index.md"))
	if err != nil {
		t.Fatal(err)
	}
	root = append(root, []byte("\n* [Storage](decisions/storage.md)\n")...)
	for i := range 30 {
		id := fmt.Sprintf("decisions/decoy-%02d", i)
		body := "---\nid: " + id + "\ntitle: Unrelated\ntype: decision\nsource_refs: [" + seedRef + "]\n---\n# Unrelated\n\nNeutral observations.\n"
		writeBaselineFile(t, workspace.Root(), "wiki/"+id+".md", body, fixed.Add(time.Duration(i)*time.Minute))
		root = append(root, []byte(fmt.Sprintf("\n* [Decoy](%s.md)\n", id))...)
	}
	writeBaselineFile(t, workspace.Root(), "wiki/index.md", string(root), fixed)
	snapshot, err := workspace.Snapshot(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Rebuild(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
}

func writeBaselineFile(t *testing.T, root, relative, content string, stamp time.Time) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func logBaseline(t *testing.T, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(encoded))
}
