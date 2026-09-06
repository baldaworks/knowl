package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

const showcaseSourceID = domain.SourceID("engineering-docs")

type showcaseTestMaintainer struct{}

func (showcaseTestMaintainer) Plan(_ context.Context, input domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	currentRef := app.SourceRefKey(input.Source)
	doc := input.Source.SourceDocument
	baseName := strings.TrimSuffix(string(doc.DocumentID), ".md")
	pageID := "entities/" + baseName
	pagePath := "wiki/" + pageID + ".md"

	refs := []string{currentRef}
	expectedDigest := ""
	for _, page := range input.Pages {
		if page.ID == domain.PageID(pageID) {
			refs = append(refs, page.SourceRefs...)
			expectedDigest = page.Digest
		}
	}

	title := strings.ReplaceAll(baseName, "-", " ")
	body := fmt.Sprintf("# %s\n\nSynthesized knowledge from %s for session revocation JWT failover.\n", title, doc.DocumentID)
	frontmatter := fmt.Sprintf("---\nid: %s\ntitle: %s\ntype: entity\nversion: \"0.2\"\nparent: catalogs/engineering\nsource_refs:\n  - %s\n---\n", pageID, title, strings.Join(refs, "\n  - "))
	content := frontmatter + body

	plan := domain.ModelEditPlan{
		SchemaDigest: input.Schema.Digest,
		SourceRefs:   refs,
		Edits: []domain.FileEdit{
			{
				Path:           pagePath,
				ExpectedDigest: expectedDigest,
				Content:        []byte(content),
			},
		},
		Rationale: "synthesize source-to-wiki showcase knowledge",
	}

	for _, catalog := range input.Catalogs {
		if catalog.Path == "wiki/index.md" {
			root := strings.TrimRight(catalog.Content, "\n") + fmt.Sprintf("\n* [%s](%s.md)\n", title, pageID)
			plan.Edits = append(plan.Edits, domain.FileEdit{
				Path:           catalog.Path,
				ExpectedDigest: catalog.Digest,
				Content:        []byte(root),
			})
			break
		}
	}

	return plan, nil
}

func TestSourceToWikiShowcaseEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	assertCheckedInShowcaseContract(t)

	// 1. Verify checked-in wiki directory is valid and complete
	checkedInWiki := filepath.Join("knowledge", "wiki")
	if _, err := os.Stat(checkedInWiki); err != nil {
		checkedInWiki = filepath.Join("..", "..", "examples", "source-to-wiki", "knowledge", "wiki")
	}
	for _, required := range []string{
		"index.md",
		"log.md",
		"catalogs/operations/index.md",
		"catalogs/security/index.md",
		"catalogs/services/index.md",
		"concepts/data-lifecycle.md",
		"concepts/incident-response.md",
		"concepts/security.md",
		"entities/acme-cloud-platform.md",
		"entities/authentication-service.md",
	} {
		fullPath := filepath.Join(checkedInWiki, required)
		if _, err := os.Stat(fullPath); err != nil {
			t.Fatalf("checked-in wiki file missing: %s (%v)", fullPath, err)
		}
	}

	// 2. Run the knowledge pipeline into a clean workspace
	workspaceDir := t.TempDir()
	workspace, err := contentfs.New(workspaceDir)
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}

	sourcesDir := "sources"
	if _, err := os.Stat(sourcesDir); err != nil {
		sourcesDir = filepath.Join("..", "..", "examples", "source-to-wiki", "sources")
	}

	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.Sources = []domain.Source{
		{
			ID:      showcaseSourceID,
			Type:    domain.SourceTypeFilesystem,
			Enabled: true,
			Config: domain.SourceConfig{
				Filesystem: &domain.FilesystemSourceConfig{
					Root:    sourcesDir,
					Include: []string{"**/*.md"},
					Flavor:  domain.SourceFlavorMarkdown,
				},
			},
		},
	}

	host, err := knowl.NewHost(ctx, config, showcaseTestMaintainer{})
	if err != nil {
		t.Fatalf("NewHost() error: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = host.Stop(stopCtx)
	}()

	runRes, err := host.RunOnce(ctx, knowl.RunOnceOptions{
		SyncSources:     true,
		DrainOperations: true,
	})
	if err != nil {
		t.Fatalf("RunOnce() error: %v", err)
	}

	if len(runRes.Sources) != 1 || runRes.Sources[0].SourceID != showcaseSourceID {
		t.Fatalf("sources result = %#v, want 1 %s", runRes.Sources, showcaseSourceID)
	}
	if runRes.Operations.Total != 4 || runRes.Operations.Completed != 4 {
		t.Fatalf("operations = %#v, want 4 total / 4 completed", runRes.Operations)
	}

	// 3. Verify that the newly run pipeline generated all expected wiki pages on disk
	for _, relPath := range []string{
		"wiki/entities/architecture-overview.md",
		"wiki/entities/authentication-service.md",
		"wiki/entities/database-retention-policy.md",
		"wiki/entities/incident-response-runbook.md",
	} {
		fullPath := filepath.Join(workspace.Root(), relPath)
		if _, err := os.Stat(fullPath); err != nil {
			t.Errorf("expected wiki file missing: %s", relPath)
		}
	}

	// 4. Verify search query retrieval with exact provenance citations
	refs, err := host.Query().Search(ctx, config.Scope, "session revocation JWT", domain.ReadLimits{Pages: 5}, []domain.SourceID{showcaseSourceID})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("expected search results for 'session revocation JWT', got 0")
	}
	if len(refs[0].SourceDocuments) == 0 {
		t.Errorf("ref %s missing source documents", refs[0].ID)
	}
}

func assertCheckedInShowcaseContract(t *testing.T) {
	t.Helper()
	artifacts := map[string][]string{
		filepath.Join("knowledge", "schema.md"): {
			"schema_version: 1", "operator-owned Markdown policy", "untrusted input",
			"Acme Cloud", "data lifecycle", "knowl.source_refs", "superseded",
		},
		filepath.Join(".config", "knowl", "config.yaml"): {
			"workspace:\n    path: knowledge", "path: .knowl/state.db",
		},
		"run.sh": {
			"${SCRIPT_DIR}/knowledge/schema.md", "${SCRIPT_DIR}/knowledge/wiki",
			"${SCRIPT_DIR}/knowledge/.knowl/bin/knowl",
		},
		"README.md": {
			"[`knowledge/schema.md`](knowledge/schema.md)", "not an executable schema", "path: .knowl/state.db",
		},
	}
	for relative, markers := range artifacts {
		content, err := os.ReadFile(relative)
		if err != nil {
			t.Fatalf("read checked-in showcase artifact %s: %v", relative, err)
		}
		for _, marker := range markers {
			if !strings.Contains(string(content), marker) {
				t.Errorf("%s missing showcase contract %q", relative, marker)
			}
		}
	}
	assertCheckedInShowcaseDigest(t)
}

func assertCheckedInShowcaseDigest(t *testing.T) {
	t.Helper()
	workspaceRoot := "knowledge"
	operationalRoot := filepath.Join(workspaceRoot, ".knowl")
	if err := os.Mkdir(operationalRoot, 0o700); err == nil {
		t.Cleanup(func() {
			if err := os.Remove(operationalRoot); err != nil && !os.IsNotExist(err) {
				t.Errorf("remove temporary showcase operational directory: %v", err)
			}
		})
	} else if !os.IsExist(err) {
		t.Fatalf("create showcase operational directory: %v", err)
	}
	workspace, err := contentfs.New(workspaceRoot)
	if err != nil {
		t.Fatalf("open checked-in showcase workspace: %v", err)
	}
	if err := workspace.Validate(); err != nil {
		t.Fatalf("validate checked-in showcase workspace: %v", err)
	}
	schema, err := os.ReadFile(filepath.Join(workspaceRoot, "schema.md"))
	if err != nil {
		t.Fatalf("read showcase schema: %v", err)
	}
	logContent, err := os.ReadFile(filepath.Join(workspaceRoot, "wiki", "log.md"))
	if err != nil {
		t.Fatalf("read showcase log: %v", err)
	}
	wantDigest := fmt.Sprintf("%x", sha256.Sum256(schema))
	matches := regexp.MustCompile(`"schema_digest":"([0-9a-f]+)"`).FindAllStringSubmatch(string(logContent), -1)
	if len(matches) == 0 {
		t.Fatal("checked-in showcase log has no schema digests")
	}
	for _, match := range matches {
		if match[1] != wantDigest {
			t.Errorf("checked-in showcase log schema digest = %q, want %q", match[1], wantDigest)
		}
	}

	allowedRefs := make(map[string]struct{})
	sourcePaths, err := filepath.Glob(filepath.Join("sources", "*.md"))
	if err != nil {
		t.Fatalf("enumerate showcase sources: %v", err)
	}
	for _, sourcePath := range sourcePaths {
		content, readErr := os.ReadFile(sourcePath)
		if readErr != nil {
			t.Fatalf("read showcase source %s: %v", sourcePath, readErr)
		}
		ref := fmt.Sprintf("wiki-filesystem:%s/%s@%x", showcaseSourceID, filepath.Base(sourcePath), sha256.Sum256(content))
		allowedRefs[ref] = struct{}{}
	}
	err = filepath.Walk(filepath.Join(workspaceRoot, "wiki"), func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || filepath.Ext(path) != ".md" || info.Name() == "index.md" || info.Name() == "log.md" {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		metadata, parseErr := knowlwiki.ParseFrontmatter(string(content))
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		if len(metadata.SourceRefs) == 0 {
			return fmt.Errorf("%s has no source refs", path)
		}
		for _, ref := range metadata.SourceRefs {
			if _, ok := allowedRefs[ref]; !ok {
				return fmt.Errorf("%s has unknown source ref %q", path, ref)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("validate checked-in showcase provenance: %v", err)
	}
}
