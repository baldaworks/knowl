package app_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/baldaworks/knowl/pkg/knowl/wiki"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
)

const portableConceptID = "concepts/portable"

type portableMaintainer struct {
	exhaust     bool
	validations int
}

func (m *portableMaintainer) Plan(context.Context, knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	return knowl.ModelEditPlan{}, errors.New("unexpected unvalidated plan")
}
func (m *portableMaintainer) PlanValidated(_ context.Context, input knowl.MaintenanceInput, limits knowl.OutputCorrectionLimits, validate func(knowl.ModelEditPlan) error) (knowl.ModelEditPlan, knowl.OperationCorrectionReport, error) {
	ref := app.SourceRefKey(input.Source)
	page := func(body string) []byte {
		return []byte(fmt.Sprintf("---\ntype: Reference\ntitle: Portable\nknowl:\n  id: concepts/portable\n  source_refs: [%s]\n---\n%s\n", ref, body))
	}
	plan := knowl.ModelEditPlan{SchemaDigest: input.Schema.Digest, SourceRefs: []string{ref}, Edits: []knowl.FileEdit{{Path: "wiki/concepts/portable.md", Content: page("[[concepts/portable]]")}}, CatalogAdditions: []knowl.CatalogAddition{{Path: testRootCatalogPath, ExpectedDigest: input.Catalogs[0].Digest, Children: []string{"wiki/concepts/portable.md"}}}}
	m.validations++
	if err := validate(plan); err == nil {
		return plan, knowl.OperationCorrectionReport{}, errors.New("accepted nonportable navigation")
	}
	bad := plan
	bad.Edits = append([]knowl.FileEdit(nil), plan.Edits...)
	if m.exhaust {
		return knowl.ModelEditPlan{}, fixtureCorrectionReport(limits, 1, knowl.CorrectionExhausted, knowl.SourcePlanInvalid, bad), app.ErrOutputCorrectionExhausted
	}
	plan.Edits[0].Content = page("[Portable](portable.md) `[[literal]]` [External](https://example.test)")
	m.validations++
	if err := validate(plan); err != nil {
		return knowl.ModelEditPlan{}, knowl.OperationCorrectionReport{}, err
	}
	return plan, fixtureCorrectionReport(limits, 2, knowl.CorrectionAccepted, knowl.SourcePlanInvalid, bad, plan), nil
}

func TestIngestCorrectsNonportableNavigation(t *testing.T) {
	for _, exhaust := range []bool{false, true} {
		t.Run(fmt.Sprint(exhaust), func(t *testing.T) {
			workspace, store, _, _ := newBaselineIngest(t)
			m := &portableMaintainer{exhaust: exhaust}
			zero := 0
			options := app.IngestOptions{AutoApply: true}
			if exhaust {
				options.Output = knowl.OutputSettings{MaxCorrections: &zero}
			}
			service, err := app.NewIngestService(workspace, store, store, m, options)
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Ingest(t.Context(), sourceEnvelope([]byte("Portable source evidence")))
			if exhaust {
				if !errors.Is(err, app.ErrOutputCorrectionExhausted) || m.validations != 1 {
					t.Fatalf("exhaustion: %v", err)
				}
				snapshot, snapshotErr := workspace.Snapshot(t.Context(), "local")
				if snapshotErr != nil || len(snapshot.Pages) != 0 {
					t.Fatalf("published rejected candidate: %#v %v", snapshot.Pages, snapshotErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if m.validations != 2 || result.Operation.Correction == nil || result.Operation.Correction.Outcome != knowl.CorrectionAccepted {
				t.Fatalf("correction evidence: %#v", result.Operation.Correction)
			}
			snapshot, err := workspace.Snapshot(t.Context(), "local")
			if err != nil || len(snapshot.Pages) != 1 {
				t.Fatalf("snapshot: %#v %v", snapshot, err)
			}
			if len(snapshot.Links) != 1 || snapshot.Links[0].From != portableConceptID || snapshot.Links[0].To != portableConceptID {
				t.Fatalf("generated graph contains fabricated literal edges: %#v", snapshot.Links)
			}
			doc, err := okf.ParseConcept("concepts/portable.md", []byte(snapshot.Pages[0].Content), okf.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			links := wiki.InspectNavigation("concepts/portable.md", doc.Body, 64)
			if links.Rule != "" || len(links.Targets) != 1 || links.Targets[0] != portableConceptID {
				t.Fatalf("published navigation: %#v", links)
			}
			destination := filepath.Join(t.TempDir(), "standalone")
			if err := workspace.ExportOKF(t.Context(), destination, contentfs.DefaultExportLimits()); err != nil {
				t.Fatal(err)
			}
			exported, err := os.ReadFile(filepath.Join(destination, "concepts", "portable.md"))
			if err != nil {
				t.Fatal(err)
			}
			standalone, err := okf.ParseConcept("concepts/portable.md", exported, okf.DefaultLimits())
			if err != nil || !reflect.DeepEqual(standalone, doc) {
				t.Fatalf("standalone metadata/content changed: %#v %v", standalone, err)
			}
			// Independently consume ordinary Markdown nodes and GitHub file URL rules.
			body := []byte(standalone.Body)
			tree := parser.New().Parse(body)
			base, err := url.Parse("https://github.com/baldaworks/knowl/blob/main/knowledge/wiki/concepts/portable.md")
			if err != nil {
				t.Fatal(err)
			}
			var internal int
			if err := ast.Walk(tree, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
				if !entering {
					return ast.WalkContinue, nil
				}
				link, ok := node.(*ast.Link)
				if !ok {
					return ast.WalkContinue, nil
				}
				target, err := url.Parse(link.Destination.Value(body))
				if err != nil {
					return ast.WalkStop, err
				}
				if target.IsAbs() {
					return ast.WalkSkipChildren, nil
				}
				resolved := base.ResolveReference(target)
				if resolved.Path != base.Path {
					t.Errorf("GitHub destination=%s want %s", resolved.Path, base.Path)
				}
				internal++
				return ast.WalkSkipChildren, nil
			}); err != nil {
				t.Fatal(err)
			}
			if internal != 1 {
				t.Fatalf("ordinary exported internal links=%d", internal)
			}
		})
	}
}
