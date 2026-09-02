package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
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

	// 1. Verify checked-in wiki directory is valid and complete
	checkedInWiki := "wiki"
	if _, err := os.Stat(checkedInWiki); err != nil {
		checkedInWiki = filepath.Join("..", "..", "examples", "source-to-wiki", "wiki")
	}
	for _, required := range []string{
		"index.md",
		"catalogs/engineering/index.md",
		"entities/architecture-overview.md",
		"entities/authentication-service.md",
		"entities/database-retention-policy.md",
		"entities/incident-response-runbook.md",
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
