package fs

import (
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
)

// Exercise the checked-in result as standalone OKF, ordinary file navigation,
// the existing export interface, and Knowl's page reader/graph.
func TestRepositoryWikiPortableBundle(t *testing.T) {
	workspace := newExportWorkspace(t)
	source := filepath.Join("..", "..", "..", "..", "knowledge", "wiki")
	docs := map[string][]byte{}
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		docs[relative] = data
		writeExportFixture(t, workspace, "wiki/"+relative, data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) == 0 {
		t.Fatal("empty corpus")
	}
	concepts := map[knowl.PageID]okf.Document{}
	expectedEdges := map[[2]knowl.PageID]bool{}
	for relative, data := range docs {
		kind, err := okf.ClassifyPath(relative)
		if err != nil {
			t.Fatal(err)
		}
		switch kind {
		case okf.DocumentConcept:
			doc, err := okf.ParseConcept(relative, data, okf.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			assertGitHubCorpusLinks(t, relative, doc.Body, docs)
			id := knowl.PageID(strings.TrimSuffix(relative, ".md"))
			concepts[id] = doc
			metadata, err := knowlwiki.FrontmatterFromMetadata(doc.Metadata)
			if err != nil || metadata.ID != string(id) || len(metadata.SourceRefs) == 0 {
				t.Fatalf("identity/provenance %s: %#v %v", relative, metadata, err)
			}
			navigation := knowlwiki.InspectNavigation(relative, doc.Body, 4096)
			if navigation.Rule != "" {
				t.Fatalf("%s: %s", relative, navigation.Rule)
			}
			for _, target := range navigation.Targets {
				// File-relative destinations resolve from the article's GitHub blob directory.
				if _, exists := docs[target+".md"]; !exists {
					t.Fatalf("%s -> absent %s", relative, target)
				}
				expectedEdges[[2]knowl.PageID{id, knowl.PageID(target)}] = true
			}
		case okf.DocumentIndex:
			index, err := okf.ValidateIndex(relative, data, okf.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			assertGitHubCorpusLinks(t, relative, index.Body, docs)
			destinations, invalid := knowlwiki.IndexDestinations(index.Body, 4096)
			if invalid {
				t.Fatal("invalid catalog navigation")
			}
			for _, destination := range destinations {
				target, external, valid := knowlwiki.ResolveIndexDestination(relative, destination)
				if !valid {
					t.Fatalf("invalid %s -> %s", relative, destination)
				}
				if !external {
					if _, exists := docs[target]; !exists {
						t.Fatalf("missing catalog target %s", target)
					}
				}
			}
		case okf.DocumentLog:
			if _, err := okf.ValidateLog(relative, data, okf.DefaultLimits()); err != nil {
				t.Fatal(err)
			}
		case okf.DocumentAsset:
		}
	}
	destination := filepath.Join(t.TempDir(), "standalone")
	if err := workspace.ExportOKF(t.Context(), destination, DefaultExportLimits()); err != nil {
		t.Fatal(err)
	}
	for relative, before := range docs {
		after, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(relative)))
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("export changed %s: %v", relative, err)
		}
	}
	snapshot, err := workspace.Snapshot(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Pages) != len(concepts) {
		t.Fatalf("snapshot pages=%d concepts=%d", len(snapshot.Pages), len(concepts))
	}
	gotEdges := map[[2]knowl.PageID]bool{}
	for _, link := range snapshot.Links {
		gotEdges[[2]knowl.PageID{link.From, link.To}] = true
	}
	if !reflect.DeepEqual(gotEdges, expectedEdges) {
		t.Fatalf("graph=%#v want %#v", gotEdges, expectedEdges)
	}
	reader := operatorReader(t, workspace)
	for id, doc := range concepts {
		page, err := reader.Page(t.Context(), testScope, id, knowl.ReadLimits{})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := okf.ParseConcept(string(id)+".md", []byte(page.Markdown), okf.DefaultLimits())
		if err != nil || !reflect.DeepEqual(parsed, doc) {
			t.Fatalf("page reader changed %s: %v", id, err)
		}
	}
}

// This consumer deliberately uses URL resolution instead of Knowl's resolver.
func assertGitHubCorpusLinks(t *testing.T, relative, markdown string, docs map[string][]byte) {
	t.Helper()
	const bundleURL = "https://github.com/baldaworks/knowl/blob/main/knowledge/wiki/"
	base, err := url.Parse(bundleURL + relative)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(markdown)
	tree := parser.New().Parse(body)
	err = ast.Walk(tree, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if _, image := node.(*ast.Image); image {
			return ast.WalkSkipChildren, nil
		}
		link, ok := node.(*ast.Link)
		if !ok {
			return ast.WalkContinue, nil
		}
		reference, err := url.Parse(link.Destination.Value(body))
		if err != nil {
			return ast.WalkStop, err
		}
		if reference.IsAbs() || reference.Host != "" || reference.Path == "" {
			return ast.WalkSkipChildren, nil
		}
		resolved := base.ResolveReference(reference)
		prefix := "/baldaworks/knowl/blob/main/knowledge/wiki/"
		if !strings.HasPrefix(resolved.Path, prefix) {
			t.Errorf("GitHub link leaves bundle: %s -> %s", relative, resolved.Path)
			return ast.WalkSkipChildren, nil
		}
		destination := strings.TrimPrefix(resolved.Path, prefix)
		if _, present := docs[destination]; !present {
			t.Errorf("GitHub link missing: %s -> %s", relative, destination)
		}
		return ast.WalkSkipChildren, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
