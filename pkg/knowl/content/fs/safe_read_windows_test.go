package fs

import (
	"path/filepath"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestCanonicalReadsWithExtendedWindowsDriveRoot(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	volume := filepath.VolumeName(workspace.Root())
	if len(volume) != 2 || volume[1] != ':' {
		t.Fatalf("test requires a Windows drive root, got %q", workspace.Root())
	}
	page := validWorkspacePage("entities/extended-root", "Extended root", testWorkspaceSourceRef, "")
	writeCanonicalFixture(t, workspace, "wiki/entities/extended-root.md", page)
	sourceContent := []byte("extended drive fixture")
	source, err := workspace.AcceptSource(t.Context(), knowl.SourceEnvelope{
		Scope: testScope, Source: knowl.SourceRef{Adapter: testFixtureAdapter, ID: "extended-drive"},
		Version: knowl.SourceVersion{Version: "1", Digest: digestBytes(sourceContent)}, Content: sourceContent, MediaType: "text/plain",
	})
	if err != nil {
		t.Fatal(err)
	}
	control, err := workspace.readControlPage(t.Context(), "index", knowl.ReadLimits{})
	if err != nil {
		t.Fatal(err)
	}
	extended, err := New(`\\?\` + workspace.Root())
	if err != nil {
		t.Fatal(err)
	}
	pages, err := extended.ReadPages(t.Context(), testScope, []knowl.PageID{"entities/extended-root"}, knowl.ReadLimits{})
	if err != nil || len(pages) != 1 || pages[0].Content != string(page) {
		t.Fatalf("extended-root pages = %#v, %v", pages, err)
	}
	gotControl, err := extended.readControlPage(t.Context(), "index", knowl.ReadLimits{})
	if err != nil || gotControl.Content != control.Content {
		t.Fatalf("extended-root control = %#v, %v", gotControl, err)
	}
	content, err := extended.ReadSource(t.Context(), source, knowl.ReadLimits{})
	if err != nil || string(content) != string(sourceContent) {
		t.Fatalf("extended-root source = %q, %v", content, err)
	}
}
