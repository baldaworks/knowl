package fs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/internal/maintenanceauthority"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestBoundMaintenanceJournalRecoveryDistinguishesPreparedAndCommitted(t *testing.T) {
	for _, point := range []string{recoveryPrepared, recoveryCommitted} {
		t.Run(point, func(t *testing.T) {
			workspace, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := workspace.Init(); err != nil {
				t.Fatal(err)
			}
			ref := acceptStructuredWorkspaceSource(t, workspace, "journal-source", "page.md", "v1")
			writeRootCatalogTargets(t, workspace, "entities/receipt.md")
			schema, err := workspace.Schema(t.Context(), testScope)
			if err != nil {
				t.Fatal(err)
			}
			if err := workspace.BindMaintenanceAuthority(maintenanceauthority.NewBinding()); err != nil {
				t.Fatal(err)
			}
			plan := knowl.ValidatedEditPlan{OperationID: "receipt-operation", Scope: testScope, SchemaDigest: schema.Digest, SourceRefs: []string{ref}, Edits: []knowl.FileEdit{{Path: "wiki/entities/receipt.md", Content: validWorkspacePage("entities/receipt", "Receipt", ref, "")}}}
			stamp := &maintenanceauthority.Stamp{Scope: testScope, Generation: strings.Repeat("a", 64), Epoch: 1}
			staged, err := workspace.StageMaintenanceBound(t.Context(), maintenanceauthority.NewStageRequest(plan, stamp))
			if err != nil {
				t.Fatal(err)
			}
			fault := errors.New("journal interruption")
			workspace.commitFault = func(current string, _ int) error {
				if current == point {
					return fault
				}
				return nil
			}
			if _, err := workspace.CommitMaintenanceBound(t.Context(), maintenanceauthority.NewCommitRequest(staged, false)); !errors.Is(err, fault) {
				t.Fatalf("fault error=%v", err)
			}
			reopened, err := New(workspace.Root())
			if err != nil {
				t.Fatal(err)
			}
			if err := reopened.BindMaintenanceAuthority(maintenanceauthority.NewBinding()); err != nil {
				t.Fatal(err)
			}
			results, err := reopened.Recover(t.Context())
			if err != nil || len(results) != 1 {
				t.Fatalf("recovery=%#v,%v", results, err)
			}
			inspection, err := reopened.InspectMaintenanceStage(t.Context(), maintenanceauthority.NewInspectRequest(staged, false))
			if err != nil {
				t.Fatal(err)
			}
			_, pageErr := os.Stat(filepath.Join(reopened.Root(), plan.Edits[0].Path))
			if point == recoveryPrepared {
				if results[0].Action != recoveryRolledBack || inspection.Committed != nil || !errors.Is(pageErr, os.ErrNotExist) {
					t.Fatalf("prepared state=%#v,receipt=%#v,page=%v", results, inspection.Committed, pageErr)
				}
			} else if results[0].Action != recoveryCompleted || inspection.Committed == nil || pageErr != nil {
				t.Fatalf("committed state=%#v,receipt=%#v,page=%v", results, inspection.Committed, pageErr)
			}
		})
	}
}

func TestLegacyAppliedPreparedJournalIsNotCommittedReceipt(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	ref := acceptStructuredWorkspaceSource(t, workspace, "legacy-journal", "page.md", "v1")
	writeRootCatalogTargets(t, workspace, "entities/legacy.md")
	schema, err := workspace.Schema(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	plan := knowl.ValidatedEditPlan{OperationID: "legacy-prepared", Scope: testScope, SchemaDigest: schema.Digest, SourceRefs: []string{ref}, Edits: []knowl.FileEdit{{Path: "wiki/entities/legacy.md", Content: validWorkspacePage("entities/legacy", "Legacy", ref, "")}}}
	staged, err := workspace.StagePlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	fault := errors.New("applied before committed marker")
	workspace.commitFault = func(point string, index int) error {
		if point == commitFaultApplied && index == len(plan.Edits) {
			return fault
		}
		return nil
	}
	if _, err := workspace.Commit(t.Context(), staged); !errors.Is(err, fault) {
		t.Fatalf("fault=%v", err)
	}
	if err := workspace.BindMaintenanceAuthority(maintenanceauthority.NewBinding()); err != nil {
		t.Fatal(err)
	}
	inspection, err := workspace.InspectMaintenanceStage(t.Context(), maintenanceauthority.NewInspectRequest(staged, false))
	if !errors.Is(err, app.ErrOperatorWorkspaceUnavailable) || inspection.Committed != nil {
		t.Fatalf("prepared stage claimed committed=%#v,%v", inspection, err)
	}
	if _, err := workspace.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	inspection, err = workspace.InspectMaintenanceStage(t.Context(), maintenanceauthority.NewInspectRequest(staged, false))
	if err != nil || inspection.Committed != nil {
		t.Fatalf("rolled-back stage claimed committed=%#v,%v", inspection, err)
	}
}
