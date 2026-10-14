package fs

import (
	"errors"
	"slices"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

func TestGeneratedNavigationValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ body, rule string }{
		{body: "[[entities/two]]", rule: knowlwiki.RuleLinkNonportable},
		{body: "[Two](/entities/two.md)", rule: knowlwiki.RuleLinkNonportable},
		{body: "[Two](../../outside.md)", rule: knowlwiki.RuleLinkUnsafe},
		{body: "[Two](two.md) `[[example]]`\n\n```md\n[[fenced]]\n```\n[URL](https://example.test)", rule: ""},
	} {
		t.Run(tc.body, func(t *testing.T) {
			t.Parallel()
			_, err := validateOrdinaryPageEdit(testPageOnePath, "entities/one", string(validWorkspacePage("entities/one", "One", testWorkspaceSourceRef, tc.body)), map[string]struct{}{testWorkspaceSourceRef: {}}, map[knowl.PageID]struct{}{"entities/two": {}}, nil)
			if tc.rule == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var invalid *contentValidationError
			if !errors.Is(err, ErrContentInvalid) || !errors.As(err, &invalid) || invalid.rule != tc.rule {
				t.Fatalf("got %v, want %s", err, tc.rule)
			}
		})
	}
}

func TestGeneratedMissingNavigationWarningStageCommitReplay(t *testing.T) {
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = workspace.Init(); err != nil {
		t.Fatal(err)
	}
	acceptWorkspaceSource(t, workspace)
	writeRootCatalogTargets(t, workspace, "entities/one.md")
	schema, err := workspace.Schema(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	plan := knowl.ValidatedEditPlan{OperationID: "missing-portable", Scope: testScope, SchemaDigest: schema.Digest, SourceRefs: []string{testWorkspaceSourceRef}, Edits: []knowl.FileEdit{{Path: testPageOnePath, Content: validWorkspacePage("entities/one", "One", testWorkspaceSourceRef, "[Missing](missing.md)")}}}
	want := []knowl.MaintenanceDiagnostic{{Code: knowl.DiagnosticLinkUnresolved, Path: testPageOnePath, Target: "entities/missing"}}
	staged, err := workspace.StagePlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(staged.Diagnostics, want) {
		t.Fatalf("warnings: %#v", staged.Diagnostics)
	}
	// Derived warnings are recomputed, never trusted from a provider/stage replay.
	plan.Diagnostics = want
	replay, err := workspace.StagePlan(t.Context(), plan)
	if err != nil || !slices.Equal(replay.Diagnostics, want) {
		t.Fatalf("replay: %#v %v", replay, err)
	}
	commit, err := workspace.Commit(t.Context(), staged)
	if err != nil || !slices.Equal(commit.Diagnostics, want) {
		t.Fatalf("commit: %#v %v", commit, err)
	}
	snapshot, err := workspace.Snapshot(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Links) != 0 {
		t.Fatalf("fabricated missing edge: %#v", snapshot.Links)
	}
}
