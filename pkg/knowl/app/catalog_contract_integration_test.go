package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	testRootCatalogPageID knowl.PageID = "index"
	testLogPageID         knowl.PageID = "log"
)

func TestIngestCatalogOverflowFailsBeforeInference(t *testing.T) {
	limits := app.DefaultCatalogLimits()
	limits.MaxCatalogs = 1
	workspace, _, service, maintainer := newWorkflowWithOptions(t, app.IngestOptions{CatalogLimits: limits}, nil)
	if err := os.MkdirAll(filepath.Join(workspace.Root(), "wiki", "topic"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace.Root(), "wiki", "topic", "index.md"), []byte("# Topic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := workspace.Snapshot(t.Context(), testSourceScope)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Ingest(t.Context(), sourceEnvelope([]byte("bounded catalog evidence")))
	if !errors.Is(err, app.ErrPlanLimitExceeded) || maintainer.calls() != 0 || result.Operation.Status != knowl.StatusFailed {
		t.Fatalf("overflow status=%s calls=%d err=%v", result.Operation.Status, maintainer.calls(), err)
	}
	after, err := workspace.Snapshot(t.Context(), testSourceScope)
	if err != nil || !reflect.DeepEqual(before.PageDigests, after.PageDigests) {
		t.Fatalf("overflow changed canonical files: %v", err)
	}
	if _, err := workspace.LoadStage(t.Context(), testSourceScope, result.Operation.ID); !errors.Is(err, app.ErrStageNotFound) {
		t.Fatalf("overflow stage=%v", err)
	}
}

func TestFilePlanRejectsRawCatalogReplacement(t *testing.T) {
	workspace, _, service, maintainer := newWorkflow(t, false, nil)
	schema, err := workspace.Schema(t.Context(), testSourceScope)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := workspace.Inspect(t.Context(), testSourceScope)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.FilePlan(t.Context(), sourceEnvelope([]byte("supplied evidence")), knowl.ModelEditPlan{
		SchemaDigest: schema.Digest, SourceRefs: []string{testSourceRef},
		Edits: []knowl.FileEdit{{Path: testRootCatalogPath, ExpectedDigest: inspection.Index.Digest, Content: []byte("# Replacement\n")}},
	})
	if !errors.Is(err, app.ErrForbiddenEdit) || maintainer.calls() != 0 || result.Operation.Status != knowl.StatusFailed {
		t.Fatalf("replacement status=%s calls=%d err=%v", result.Operation.Status, maintainer.calls(), err)
	}
	after, err := workspace.Inspect(t.Context(), testSourceScope)
	if err != nil || after.Index.Content != inspection.Index.Content || after.Index.Digest != inspection.Index.Digest {
		t.Fatalf("supplied plan overwrote catalog: %v", err)
	}
}

// Only context selection is controlled; source acceptance, reads, validation and staging are real.
type mixedCatalogContext struct {
	app.SearchIndex
}

func (mixedCatalogContext) SelectContext(context.Context, knowl.ScopeRef, knowl.SourceSummary, knowl.ReadLimits) ([]knowl.PageID, error) {
	return []knowl.PageID{testRootCatalogPageID, "decisions/index", testLogPageID, "decisions/storage"}, nil
}

func TestIngestFiltersCatalogAndControlContextBeforeReads(t *testing.T) {
	workspace, store, maintainer, _ := newBaselineIngest(t)
	seedBaselineContext(t, workspace, store)
	writeFixturePage(t, workspace.Root(), "wiki/decisions/index.md", []byte("# Decisions\n* [Storage](storage.md)\n"), time.Unix(0, 0))
	rootPath := filepath.Join(workspace.Root(), testRootCatalogPath)
	root, err := os.ReadFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	root = append(root, []byte("\n* [Decisions](decisions/index.md)\n")...)
	if err := os.WriteFile(rootPath, root, 0o600); err != nil {
		t.Fatal(err)
	}
	limits := app.DefaultReadLimits()
	// Root is larger than this page ceiling, so passing it to ReadPages would fail.
	limits.Bytes = 512
	limits.Characters = 512
	if len(root) <= limits.Bytes {
		t.Fatal("fixture must exceed factual read ceiling")
	}
	service, err := app.NewIngestService(workspace, store, mixedCatalogContext{SearchIndex: store}, maintainer, app.IngestOptions{ReadLimits: limits})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Ingest(t.Context(), sourceEnvelope([]byte("mixed selected context"))); err != nil {
		t.Fatal(err)
	}
	if len(maintainer.input.Pages) != 1 || maintainer.input.Pages[0].ID != "decisions/storage" || len(maintainer.input.Catalogs) != 2 {
		t.Fatalf("unfiltered context: pages=%v catalogs=%v", maintainer.input.Pages, maintainer.input.Catalogs)
	}
}
