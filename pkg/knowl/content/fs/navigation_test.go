package fs

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

const portableCrossPageID = "entities/two"

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
			_, err := validateOrdinaryPageEdit(testPageOnePath, testPageOneID, string(validWorkspacePage(testPageOneID, "One", testWorkspaceSourceRef, tc.body)), map[string]struct{}{testWorkspaceSourceRef: {}}, map[knowl.PageID]struct{}{portableCrossPageID: {}}, nil)
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
	plan := knowl.ValidatedEditPlan{OperationID: "missing-portable", Scope: testScope, SchemaDigest: schema.Digest, SourceRefs: []string{testWorkspaceSourceRef}, Edits: []knowl.FileEdit{{Path: testPageOnePath, Content: validWorkspacePage(testPageOneID, "One", testWorkspaceSourceRef, "[Missing](missing.md)")}}}
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

func TestPortableSnapshotNavigationExcludesLiteralCode(t *testing.T) {
	workspace := newExportWorkspace(t)
	bodies := map[string]string{
		"concepts/deep/article": "[Same](other.md#part) [Nested](nested/leaf.md) [Cross](../../entities/two.md) [Unicode](%D0%9E%D0%B1%D0%B7%D0%BE%D1%80.md) [Missing](missing.md) [External](https://example.test) ![Image [Description link](code.md)](image.png) `[[concepts/deep/code]]`\n\n`[Inline](code.md)`\n\n    [Indented](code.md)\n\n```md\n[Fenced](code.md)\n```\n",
		"concepts/deep/other":   "Other", "concepts/deep/nested/leaf": "Nested", portableCrossPageID: "Cross", "concepts/deep/Обзор": "Unicode", "concepts/deep/code": "Code target exists",
	}
	for id, body := range bodies {
		writeExportFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, testWorkspaceSourceRef, body))
	}
	snapshot, err := workspace.Snapshot(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	got := map[knowl.PageID]bool{}
	for _, link := range snapshot.Links {
		if link.From == "concepts/deep/article" {
			got[link.To] = true
		}
	}
	want := map[knowl.PageID]bool{"concepts/deep/other": true, "concepts/deep/nested/leaf": true, portableCrossPageID: true, "concepts/deep/Обзор": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projected edges=%#v want %#v", got, want)
	}
}
