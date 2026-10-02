package app_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/baldaworks/knowl/pkg/knowl/wiki"
)

func TestCatalogAdditionCanonicalCommitAndStaleOriginal(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "stale original"}[stale], func(t *testing.T) {
			workspace, _, _, _ := newWorkflow(t, false, nil)
			accepted, err := workspace.AcceptSource(t.Context(), sourceEnvelope([]byte("navigation evidence")))
			if err != nil {
				t.Fatal(err)
			}
			schema, err := workspace.Schema(t.Context(), accepted.Scope)
			if err != nil {
				t.Fatal(err)
			}
			rootPath := filepath.Join(workspace.Root(), testRootCatalogPath)
			original, err := os.ReadFile(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			original = append(original, []byte("\n* [External](https://example.test/)\n  Operator notes remain.\n")...)
			if err := os.WriteFile(rootPath, original, 0o600); err != nil {
				t.Fatal(err)
			}
			inspection, err := workspace.Inspect(t.Context(), accepted.Scope)
			if err != nil {
				t.Fatal(err)
			}
			const subject = "wiki/catalogs/review/index.md"
			model := knowl.ModelEditPlan{SchemaDigest: schema.Digest, SourceRefs: []string{testSourceRef}, Edits: []knowl.FileEdit{{Path: testPagePath, Content: planPageContent}, {Path: testPageTwoPath, Content: planSupportingContent}}, CatalogAdditions: []knowl.CatalogAddition{
				{Path: testRootCatalogPath, ExpectedDigest: inspection.Index.Digest, Children: []string{subject}},
				{Path: subject, Title: "Review `notes", Children: []string{testPagePath, testPageTwoPath}},
			}}
			plan, err := app.ValidateMaintenancePlan(t.Context(), knowl.MaintenanceInput{Scope: accepted.Scope, Schema: schema, Source: accepted}, model, inspection, app.DefaultCatalogLimits(), app.DefaultPlanLimits())
			if err != nil {
				t.Fatal(err)
			}
			staged, err := workspace.StagePlan(t.Context(), plan)
			if err != nil {
				t.Fatalf("real canonical stage: %v", err)
			}
			concurrent := append(append([]byte(nil), original...), []byte("\n  Concurrent operator note.\n")...)
			if stale {
				if err := os.WriteFile(rootPath, concurrent, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err = workspace.Commit(t.Context(), staged)
			if stale {
				if !errors.Is(err, contentfs.ErrPrecondition) {
					t.Fatalf("stale commit=%v", err)
				}
				got, readErr := os.ReadFile(rootPath)
				if readErr != nil || string(got) != string(concurrent) {
					t.Fatal("concurrent root was overwritten")
				}
				if _, err := os.Stat(filepath.Join(workspace.Root(), testPagePath)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("rejected plan partially committed")
				}
				return
			}
			if err != nil {
				t.Fatalf("real canonical commit: %v", err)
			}
			after, err := workspace.Inspect(t.Context(), accepted.Scope)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(after.Index.Content, string(original)) {
				t.Fatal("operator content was rewritten")
			}
			var subjectContent string
			for _, catalog := range after.Catalogs {
				if catalog.Path == subject {
					subjectContent = catalog.Content
				}
			}
			destinations, malformed := wiki.IndexDestinations(subjectContent, 3)
			if malformed || len(destinations) != 2 {
				t.Fatalf("canonical nested links=%v malformed=%v", destinations, malformed)
			}
			for _, path := range []string{testPagePath, testPageTwoPath} {
				found := false
				for _, destination := range destinations {
					target, external, valid := wiki.ResolveIndexDestination(strings.TrimPrefix(subject, "wiki/"), destination)
					found = found || (valid && !external && "wiki/"+target == path)
				}
				if !found {
					t.Fatalf("missing committed navigation to %s", path)
				}
			}
		})
	}
}
