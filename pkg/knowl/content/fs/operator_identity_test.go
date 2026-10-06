package fs

import (
	"errors"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestOperatorRequiresCanonicalIDs(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	const pageID = "entities/canonical.name"
	content := validWorkspacePage(pageID, "Canonical", testWorkspaceSourceRef, "Body")
	writeCanonicalFixture(t, workspace, "wiki/"+pageID+".md", content)
	writeCanonicalFixture(t, workspace, "wiki/catalogs/team/index.md", []byte("# Team\n\n* [Page](../../entities/canonical.name.md)\n"))
	service, err := app.NewOperatorService(testScope, app.OperatorReaders{Page: workspace, Pages: workspace, Catalogs: workspace}, app.OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []knowl.PageID{pageID + ".md", "wiki/" + pageID, "wiki/" + pageID + ".md"} {
		t.Run(string(id), func(t *testing.T) {
			if _, err := service.Page(t.Context(), id); !errors.Is(err, app.ErrOperatorInvalidRequest) {
				t.Errorf("operator page alias = %v, want invalid request", err)
			}
			// Required legacy reads retain filename/prefix aliases. Their existing
			// parser requires a Markdown suffix for dotted filenames.
			if id == "wiki/"+pageID {
				return
			}
			pages, err := workspace.ReadPages(t.Context(), testScope, []knowl.PageID{id}, knowl.ReadLimits{})
			if err != nil || len(pages) != 1 || pages[0].Content != string(content) {
				t.Fatalf("legacy page alias = %#v, %v", pages, err)
			}
		})
	}
	for _, parent := range []knowl.PageID{"index.md", "wiki/index", "wiki/index.md", "catalogs/team/index.md", "wiki/catalogs/team/index"} {
		if _, err := service.CatalogChildren(t.Context(), parent, app.OperatorListOptions{}); !errors.Is(err, app.ErrOperatorInvalidRequest) {
			t.Errorf("operator catalog alias %q = %v, want invalid request", parent, err)
		}
	}

	page, err := service.Page(t.Context(), pageID)
	if err != nil || page.ID != pageID || page.Markdown != string(content) {
		t.Fatalf("canonical page = %#v, %v", page, err)
	}
	summaries, err := service.PageSummaries(t.Context(), app.OperatorListOptions{})
	if err != nil || len(summaries.Items) != 1 || summaries.Items[0].ID != page.ID {
		t.Fatalf("canonical summaries = %#v, %v", summaries, err)
	}
	for _, parent := range []knowl.PageID{"", operatorRootID, operatorTestCatalogID} {
		catalog, err := service.CatalogChildren(t.Context(), parent, app.OperatorListOptions{})
		want := parent
		if want == "" {
			want = operatorRootID
		}
		if err != nil || catalog.Parent.ID != want {
			t.Errorf("canonical catalog %q = %#v, %v", parent, catalog, err)
		}
	}
}
