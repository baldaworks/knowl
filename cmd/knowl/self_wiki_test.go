package main

import (
	"context"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	sourcefs "github.com/baldaworks/knowl/internal/source/filesystem"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

func TestCheckedInSelfWikiContract(t *testing.T) {
	repoRoot := testRepoRoot(t)
	t.Chdir(repoRoot)
	clearKnowlEnv(t)

	ctx, err := loadConfig(context.Background(), "", "")
	if err != nil {
		t.Fatalf("load checked-in config: %v", err)
	}
	config, err := hostConfig(ctx)
	if err != nil {
		t.Fatalf("normalize checked-in config: %v", err)
	}
	if len(config.Sources) != 1 {
		t.Fatalf("sources = %d, want exactly one", len(config.Sources))
	}
	source := config.Sources[0]
	if source.ID != "knowl-docs" || source.Config.Filesystem == nil {
		t.Fatalf("self-wiki source = %#v", source)
	}
	wantRoot := filepath.Join(repoRoot, "docs")
	if source.Config.Filesystem.Root != wantRoot || !reflect.DeepEqual(source.Config.Filesystem.Include, []string{"**/*.md"}) {
		t.Fatalf("self-wiki filesystem config = %#v, want root %q and docs/**/*.md", source.Config.Filesystem, wantRoot)
	}

	page, err := sourcefs.NewDefault().List(context.Background(), source, "")
	if err != nil {
		t.Fatalf("enumerate self-wiki source: %v", err)
	}
	if page.NextPageToken != "" {
		t.Fatal("self-wiki docs unexpectedly exceed one bounded source page")
	}
	gotDocuments := make([]string, 0, len(page.Documents))
	for _, document := range page.Documents {
		gotDocuments = append(gotDocuments, document.Path)
		if strings.HasPrefix(document.Path, "knowledge/") {
			t.Fatalf("generated output entered source discovery: %q", document.Path)
		}
	}
	wantDocuments := nonHiddenMarkdownFiles(t, wantRoot)
	if !reflect.DeepEqual(gotDocuments, wantDocuments) {
		t.Fatalf("source documents = %#v, want %#v", gotDocuments, wantDocuments)
	}
	allowedDocuments := make(map[string]struct{}, len(wantDocuments))
	for _, document := range wantDocuments {
		allowedDocuments[document] = struct{}{}
	}

	workspaceRoot := filepath.Join(repoRoot, "knowledge")
	ensureSelfWikiOperationalDir(t, workspaceRoot)
	workspace, err := contentfs.New(workspaceRoot)
	if err != nil {
		t.Fatalf("open checked-in self-wiki: %v", err)
	}
	if err := workspace.Validate(); err != nil {
		t.Fatalf("validate checked-in self-wiki: %v", err)
	}

	ordinaryPages := selfWikiOrdinaryPages(t, filepath.Join(repoRoot, "knowledge", "wiki"))
	if len(ordinaryPages) == 0 {
		t.Fatal("checked-in self-wiki has no ordinary pages")
	}
	pageIDs := make(map[string]struct{}, len(ordinaryPages))
	var sourceRefs []string
	for _, pagePath := range ordinaryPages {
		content, readErr := os.ReadFile(pagePath)
		if readErr != nil {
			t.Fatalf("read ordinary page %s: %v", pagePath, readErr)
		}
		metadata, parseErr := knowlwiki.ParseFrontmatter(string(content))
		if parseErr != nil {
			t.Fatalf("parse ordinary page %s: %v", pagePath, parseErr)
		}
		if len(metadata.SourceRefs) == 0 {
			t.Fatalf("ordinary page %s has no provenance", pagePath)
		}
		pageIDs[metadata.ID] = struct{}{}
		for _, sourceRef := range metadata.SourceRefs {
			assertSelfWikiSourceRef(t, pagePath, sourceRef, allowedDocuments)
		}
		sourceRefs = append(sourceRefs, metadata.SourceRefs...)
	}
	for _, pageID := range []string{
		"concepts/product-architecture",
		"concepts/service-operations",
		"concepts/workspace-semantics",
		"concepts/sidecar-deployment",
		"concepts/semantic-source-maintenance",
	} {
		if _, exists := pageIDs[pageID]; !exists {
			t.Errorf("self-wiki missing representative semantic page %q", pageID)
		}
	}
	joinedRefs := strings.Join(sourceRefs, "\n")
	for _, marker := range []string{"/design.md@", "/operations.md@", "/workspace.md@", "/sidecar.md@", "/releases/"} {
		if !strings.Contains(joinedRefs, marker) {
			t.Errorf("ordinary-page provenance does not cover %q", marker)
		}
	}

	for _, artifact := range []string{"Taskfile.yml", filepath.Join("knowledge", "schema.md"), filepath.Join("knowledge", "wiki", "index.md"), filepath.Join("knowledge", "wiki", "log.md")} {
		if _, err := os.Stat(filepath.Join(repoRoot, artifact)); err != nil {
			t.Errorf("required self-wiki artifact %s: %v", artifact, err)
		}
	}
	readme, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	for _, required := range []string{"knowledge/wiki/index.md", "docs/**/*.md", "task wiki:generate", "task wiki:validate", "knowledge/.knowl/**", "model-dependent", "knowl.source_refs"} {
		if !strings.Contains(string(readme), required) {
			t.Errorf("README missing self-wiki contract %q", required)
		}
	}
}

func ensureSelfWikiOperationalDir(t *testing.T, workspaceRoot string) {
	t.Helper()
	operationalDir := filepath.Join(workspaceRoot, ".knowl")
	err := os.Mkdir(operationalDir, 0o700)
	if err == nil {
		t.Cleanup(func() {
			if err := os.Remove(operationalDir); err != nil && !os.IsNotExist(err) {
				t.Errorf("remove temporary operational directory: %v", err)
			}
		})
		return
	}
	if !os.IsExist(err) {
		t.Fatalf("create temporary operational directory: %v", err)
	}
}

func selfWikiOrdinaryPages(t *testing.T, root string) []string {
	t.Helper()
	var pages []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return nil
		}
		switch entry.Name() {
		case "index.md", "log.md":
			return nil
		default:
			pages = append(pages, path)
			return nil
		}
	})
	if err != nil {
		t.Fatalf("walk ordinary self-wiki pages: %v", err)
	}
	sort.Strings(pages)
	return pages
}

func assertSelfWikiSourceRef(t *testing.T, pagePath, sourceRef string, allowedDocuments map[string]struct{}) {
	t.Helper()
	const prefix = "wiki-filesystem:knowl-docs/"
	remainder, ok := strings.CutPrefix(sourceRef, prefix)
	if !ok {
		t.Fatalf("ordinary page %s has non-docs provenance %q", pagePath, sourceRef)
	}
	separator := strings.LastIndexByte(remainder, '@')
	if separator <= 0 || separator == len(remainder)-1 {
		t.Fatalf("ordinary page %s has malformed provenance %q", pagePath, sourceRef)
	}
	document, revision := remainder[:separator], remainder[separator+1:]
	if _, exists := allowedDocuments[document]; !exists {
		t.Fatalf("ordinary page %s cites disallowed document %q", pagePath, document)
	}
	decoded, err := hex.DecodeString(revision)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("ordinary page %s has invalid immutable revision in %q", pagePath, sourceRef)
	}
}

func nonHiddenMarkdownFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs tree: %v", err)
	}
	sort.Strings(files)
	return files
}
