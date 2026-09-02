package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	knowl "github.com/baldaworks/knowl/pkg/knowl"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	sourceEngineeringID = domain.SourceID("engineering-docs")
)

type showcaseResult struct {
	RunResult knowl.RunOnceResult
	Evidence  map[string][]domain.PageReference
}

func runKnowledgeShowcase(ctx context.Context, workspaceRoot string, sourcesRoot string, maintainer any) (showcaseResult, error) {
	if workspaceRoot == "" {
		tmpDir, err := os.MkdirTemp("", "knowl-showcase-ws-*")
		if err != nil {
			return showcaseResult{}, fmt.Errorf("create workspace temp dir: %w", err)
		}
		workspaceRoot = tmpDir
	}

	workspace, err := contentfs.New(workspaceRoot)
	if err != nil {
		return showcaseResult{}, fmt.Errorf("open workspace: %w", err)
	}
	if err := workspace.Init(); err != nil {
		return showcaseResult{}, fmt.Errorf("init workspace: %w", err)
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
					Root:    sourcesRoot,
					Include: []string{"**/*.md"},
					Flavor:  domain.SourceFlavorMarkdown,
				},
			},
		},
	}

	hostOpts := knowl.Options{Config: config}
	if m, ok := maintainer.(knowl.Options); ok {
		hostOpts = m
		hostOpts.Config = config
	}

	host, err := knowl.New(ctx, hostOpts)
	if err != nil {
		return showcaseResult{}, fmt.Errorf("compose knowl host: %w", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
		defer cancel()
		_ = host.Stop(stopCtx)
	}()

	// 1. Execute one-shot knowledge processing cycle
	runRes, err := host.RunOnce(ctx, knowl.RunOnceOptions{
		SyncSources:     true,
		DrainOperations: true,
	})
	if err != nil {
		return showcaseResult{}, fmt.Errorf("run-once knowledge cycle: %w", err)
	}

	// 2. Query knowledge base for sample questions
	sampleQuestions := []string{
		"session authentication and token revocation",
		"database failover incident response",
	}

	evidenceMap := make(map[string][]domain.PageReference)
	for _, query := range sampleQuestions {
		results, err := host.Query().Search(ctx, config.Scope, query, domain.ReadLimits{Pages: 5}, []domain.SourceID{sourceEngineeringID})
		if err != nil {
			return showcaseResult{}, fmt.Errorf("search query %q: %w", query, err)
		}
		evidenceMap[query] = results
	}

	return showcaseResult{
		RunResult: runRes,
		Evidence:  evidenceMap,
	}, nil
}

func printShowcaseSummary(res showcaseResult) {
	fmt.Printf("=== One-Shot Knowledge Pipeline Summary ===\n")
	for _, src := range res.RunResult.Sources {
		fmt.Printf("Source %s: changed=%v (added=%d, updated=%d, unchanged=%d)\n",
			src.SourceID, src.Changed, src.Run.Counts.Added, src.Run.Counts.Updated, src.Run.Counts.Unchanged)
	}
	fmt.Printf("Maintenance Operations: %d total, %d completed, %d failed\n\n",
		res.RunResult.Operations.Total,
		res.RunResult.Operations.Completed,
		res.RunResult.Operations.Failed,
	)

	fmt.Printf("=== Knowledge Retrieval & Provenance Evidence ===\n")
	for query, refs := range res.Evidence {
		fmt.Printf("Query: %q\n", query)
		if len(refs) == 0 {
			fmt.Println("  (no direct lexical matches found)")
			continue
		}
		for _, ref := range refs {
			fmt.Printf("  • Page: %s (%s)\n", ref.Title, ref.ID)
			fmt.Printf("    Source Documents: %s\n", formatSourceDocuments(ref.SourceDocuments))
			if ref.Snippet != "" {
				fmt.Printf("    Snippet: %s\n", strings.ReplaceAll(ref.Snippet, "\n", " "))
			}
		}
	}
}

func formatSourceDocuments(docs []domain.SourceDocument) string {
	if len(docs) == 0 {
		return "none"
	}
	var b strings.Builder
	for i, d := range docs {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s/%s@%s", d.SourceID, d.DocumentID, truncateRevision(d.Revision))
	}
	return b.String()
}

func truncateRevision(rev string) string {
	if len(rev) > 8 {
		return rev[:8]
	}
	return rev
}
