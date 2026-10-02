package app_test

import (
	"context"
	"encoding/json"
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
				if result.Outcome != baselineMet {
					t.Fatalf("source signal recall must succeed: %#v", result)
				}
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
	result, err := service.Ingest(t.Context(), sourceEnvelope([]byte("# Catalog maintenance")))
	if err != nil {
		t.Fatalf("32-catalog ingest must succeed at factual Pages=20: %v", err)
	}
	if _, err := service.Apply(t.Context(), result.Operation.Key.Scope, result.Operation.ID); err != nil {
		t.Fatalf("32-catalog commit: %v", err)
	}
	if len(maintainer.input.Catalogs) != 32 {
		t.Fatal("successful ingest omitted catalogs")
	}
	for _, p := range maintainer.input.Pages {
		if filepath.Base(p.Path) == "index.md" || filepath.Base(p.Path) == "log.md" {
			t.Fatal("catalog/control Markdown leaked through factual pages")
		}
	}
	if maintainer.input.Limits.Pages != 20 || maintainer.input.CatalogLimits.MaxCatalogs != 1024 || maintainer.input.ContractVersion != "source-maintenance-v3" {
		t.Fatal("incorrect independent limits/contract")
	}
	for i, node := range maintainer.input.Catalogs {
		expected := testRootCatalogPath
		if i > 0 {
			expected = fmt.Sprintf("wiki/catalog-%02d/index.md", i-1)
		}
		if node.Path != expected || node.Digest == "" {
			t.Fatalf("graph node%d=%v", i, node)
		}
	}
	expectedChildren := make([]string, 31)
	for i := range expectedChildren {
		expectedChildren[i] = fmt.Sprintf("wiki/catalog-%02d/index.md", i)
	}
	if !reflect.DeepEqual(maintainer.input.Catalogs[0].Children, expectedChildren) {
		t.Fatal("incomplete root edges")
	}
	outcome := baselineMet
	logBaseline(t, struct {
		CaseID   string `json:"case_id"`
		Catalogs int    `json:"catalogs"`
		Pages    int    `json:"page_limit"`
		Outcome  string `json:"outcome"`
	}{"catalog-scaling", 32, 20, outcome})
	after, snapshotErr := workspace.Snapshot(t.Context(), "local")
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	// Commit records its normal log entry while no-op navigation/factual content stays unchanged.
	delete(before.PageDigests, "wiki/log.md")
	delete(after.PageDigests, "wiki/log.md")
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
