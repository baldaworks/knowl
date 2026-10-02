package app_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const visibilityWrongDigest = "wrong_digest"

func TestFilePlanRequiresIncludedCompleteExistingSnapshot(t *testing.T) {
	for _, mode := range []string{"omitted", "budget_omitted", "included", visibilityWrongDigest} {
		t.Run(mode, func(t *testing.T) {
			workspace, store, maintainer, _ := newBaselineIngest(t)
			seedBaselineContext(t, workspace, store)
			ids := []knowl.PageID{budgetStorageID}
			if mode == "omitted" {
				ids = nil
			}
			options := app.IngestOptions{AutoApply: true}
			if mode == "budget_omitted" {
				full, readErr := os.ReadFile(filepath.Join(workspace.Root(), "wiki/decisions/storage.md"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				if err := os.WriteFile(filepath.Join(workspace.Root(), "wiki/decisions/storage.md"), append(full, []byte(strings.Repeat("unrelated preserved prose ", 1000))...), 0o600); err != nil {
					t.Fatal(err)
				}
				options.InputLimits.MaxRequestBytes = 8000
			}
			service, err := app.NewIngestService(workspace, store, orderedBudgetIndex{store, ids}, maintainer, options)
			if err != nil {
				t.Fatal(err)
			}
			pages, err := workspace.ReadPages(t.Context(), "local", []knowl.PageID{budgetStorageID}, app.DefaultReadLimits())
			if err != nil {
				t.Fatal(err)
			}
			original := pages[0]
			// Keep unrelated prose and the old citation while adding independently accepted evidence.
			content := strings.Replace(original.Content, "source_refs: [fixture:baseline-seed@1]", "source_refs: [fixture:baseline-seed@1, "+testSourceRef+"]", 1) + "\nNew independently accepted evidence.\n"
			schema, err := workspace.Schema(t.Context(), "local")
			if err != nil {
				t.Fatal(err)
			}
			expected := original.Digest
			if mode == visibilityWrongDigest {
				expected = strings.Repeat("f", 64)
			}
			plan := knowl.ModelEditPlan{SchemaDigest: schema.Digest, SourceRefs: []string{"fixture:baseline-seed@1", testSourceRef}, Edits: []knowl.FileEdit{{Path: original.Path, ExpectedDigest: expected, Content: []byte(content)}}}
			before, err := workspace.Snapshot(t.Context(), "local")
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.FilePlan(t.Context(), sourceEnvelope([]byte("new evidence")), plan)
			if mode == "included" {
				if err != nil || result.Operation.Status != knowl.StatusCommitted {
					t.Fatalf("included status=%s err=%v", result.Operation.Status, err)
				}
				after, readErr := workspace.ReadPages(t.Context(), "local", []knowl.PageID{budgetStorageID}, app.DefaultReadLimits())
				if readErr != nil || len(after) != 1 || after[0].Content != content || !reflect.DeepEqual(after[0].SourceRefs, []string{"fixture:baseline-seed@1", testSourceRef}) {
					t.Fatalf("updated snapshot=%+v err=%v", after, readErr)
				}
			} else {
				want := app.ErrForbiddenEdit
				if mode == visibilityWrongDigest {
					want = app.ErrPlanInvalid
				}
				if !errors.Is(err, want) || result.Operation.Status != knowl.StatusFailed {
					t.Fatalf("mode=%s status=%s err=%v", mode, result.Operation.Status, err)
				}
				after, snapshotErr := workspace.Snapshot(t.Context(), "local")
				if snapshotErr != nil || !reflect.DeepEqual(before.PageDigests, after.PageDigests) {
					t.Fatalf("rejected edit mutated canonical: %v", snapshotErr)
				}
				full, readErr := os.ReadFile(filepath.Join(workspace.Root(), original.Path))
				if readErr != nil || string(full) != original.Content {
					t.Fatalf("original changed: %v", readErr)
				}
				if _, stageErr := workspace.LoadStage(t.Context(), "local", result.Operation.ID); !errors.Is(stageErr, app.ErrStageNotFound) {
					t.Fatalf("rejected stage=%v", stageErr)
				}
			}
		})
	}
}
