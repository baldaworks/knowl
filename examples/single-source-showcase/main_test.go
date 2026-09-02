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

type showcaseMaintainer struct{}

func (showcaseMaintainer) Plan(_ context.Context, input domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	currentRef := app.SourceRefKey(input.Source)
	doc := input.Source.SourceDocument
	pageID := "entities/" + strings.TrimSuffix(string(doc.DocumentID), ".md")
	pagePath := "wiki/" + pageID + ".md"

	refs := []string{currentRef}
	expectedDigest := ""
	for _, page := range input.Pages {
		if page.ID == domain.PageID(pageID) {
			refs = append(refs, page.SourceRefs...)
			expectedDigest = page.Digest
		}
	}

	title := strings.ReplaceAll(strings.TrimSuffix(string(doc.DocumentID), ".md"), "-", " ")
	body := fmt.Sprintf("# %s\n\nSynthesized knowledge from %s for user session and failover operations.\n", title, doc.DocumentID)
	frontmatter := fmt.Sprintf("---\nid: %s\ntitle: %s\ntype: entity\nsource_refs:\n  - %s\n---\n", pageID, title, strings.Join(refs, "\n  - "))
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
		Rationale: "synthesize single source showcase knowledge",
	}

	for _, catalog := range input.Catalogs {
		if catalog.Path == "wiki/index.md" {
			root := strings.TrimRight(catalog.Content, "\n") + fmt.Sprintf("\n\n* [%s](%s.md)\n", title, pageID)
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

func TestSingleSourceShowcaseEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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
		sourcesDir = filepath.Join("..", "..", "examples", "single-source-showcase", "sources")
	}

	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.Sources = []domain.Source{
		{
			ID:      sourceEngineeringID,
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

	host, err := knowl.NewHost(ctx, config, showcaseMaintainer{})
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

	if len(runRes.Sources) != 1 || runRes.Sources[0].SourceID != sourceEngineeringID {
		t.Fatalf("sources result = %#v, want 1 %s", runRes.Sources, sourceEngineeringID)
	}
	if runRes.Operations.Total != 4 || runRes.Operations.Completed != 4 {
		t.Fatalf("operations = %#v, want 4 total / 4 completed", runRes.Operations)
	}

	// Verify all 4 wiki concepts were generated on disk
	expectedFiles := []string{
		"wiki/entities/architecture-overview.md",
		"wiki/entities/authentication-service.md",
		"wiki/entities/database-retention-policy.md",
		"wiki/entities/incident-response-runbook.md",
	}
	for _, relPath := range expectedFiles {
		fullPath := filepath.Join(workspace.Root(), relPath)
		if _, err := os.Stat(fullPath); err != nil {
			t.Errorf("expected wiki file missing: %s", relPath)
		}
	}

	// Verify query retrieval
	queries := []string{
		"authentication session",
		"incident response failover",
	}
	for _, q := range queries {
		refs, err := host.Query().Search(ctx, config.Scope, q, domain.ReadLimits{Pages: 5}, []domain.SourceID{sourceEngineeringID})
		if err != nil {
			t.Fatalf("Search(%q) error: %v", q, err)
		}
		if len(refs) == 0 {
			t.Errorf("Search(%q) returned 0 results", q)
		}
		for _, ref := range refs {
			if len(ref.SourceDocuments) == 0 {
				t.Errorf("Search(%q) ref %s missing source documents", q, ref.ID)
			}
		}
	}
}
