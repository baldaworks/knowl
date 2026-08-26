package main

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	testDecisionID    = domain.PageID("decisions/session-memory")
	testDecisionPath  = "wiki/decisions/session-memory.md"
	testRunbookID     = domain.PageID("runbooks/session-recovery")
	testRunbookPath   = "wiki/runbooks/session-recovery.md"
	testOperatorToken = "project-decisions-test-token"
)

func TestProjectDecisionsHostRunsThroughAuthenticatedMCPAndReplays(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("New workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("Init workspace: %v", err)
	}
	maintainer := &projectMaintainer{}
	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.ListenAddr = "127.0.0.1:0"
	config.OperatorToken = testOperatorToken
	host, err := knowl.NewHost(ctx, config, maintainer)
	if err != nil {
		t.Fatalf("NewHost(): %v", err)
	}
	if err := host.Start(ctx); err != nil {
		t.Fatalf("Start(): %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := host.Stop(shutdownCtx); err != nil {
			t.Errorf("Stop(): %v", err)
		}
	})

	client := clientConfig{
		Endpoint: "http://" + host.Addr() + "/mcp", OperatorToken: testOperatorToken,
		PollInterval: time.Millisecond,
	}
	first, err := runProjectDecisions(ctx, client)
	if err != nil {
		t.Fatalf("first host run: %v", err)
	}
	assertProjectResult(t, first)
	answer, err := hostAnswer(first)
	if err != nil || !strings.HasPrefix(answer, "Host answer") || !strings.Contains(answer, "inline:adr-session-memory@1") {
		t.Fatalf("hostAnswer() = %q, %v", answer, err)
	}
	if maintainer.Calls() != len(sourceManifest) {
		t.Fatalf("maintainer calls = %d, want %d", maintainer.Calls(), len(sourceManifest))
	}

	second, err := runProjectDecisions(ctx, client)
	if err != nil {
		t.Fatalf("replayed host run: %v", err)
	}
	assertProjectResult(t, second)
	if maintainer.Calls() != len(sourceManifest) {
		t.Fatalf("replay maintainer calls = %d, want %d", maintainer.Calls(), len(sourceManifest))
	}
	for index := range first.Operations {
		if first.Operations[index].OperationID != second.Operations[index].OperationID {
			t.Fatalf("replay operation %d changed: first=%#v second=%#v", index, first.Operations[index], second.Operations[index])
		}
	}
}

func assertProjectResult(t *testing.T, result runResult) {
	t.Helper()
	if len(result.Operations) != len(sourceManifest) {
		t.Fatalf("operations = %d, want %d", len(result.Operations), len(sourceManifest))
	}
	for _, operation := range result.Operations {
		if operation.OperationID == "" || operation.Status != "completed" {
			t.Fatalf("non-terminal operation: %#v", operation)
		}
	}
	for _, evidence := range result.Evidence {
		if evidence.PageID != string(testDecisionID) {
			continue
		}
		if !evidence.Untrusted || !strings.Contains(strings.ToLower(evidence.Snippet), "badger") || len(evidence.SourceRefs) != 3 {
			t.Fatalf("decision evidence = %#v", evidence)
		}
		return
	}
	t.Fatalf("session-memory decision missing from evidence: %#v", result.Evidence)
}

type projectMaintainer struct {
	mu    sync.Mutex
	calls int
}

func (maintainer *projectMaintainer) Plan(ctx context.Context, input domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	if err := ctx.Err(); err != nil {
		return domain.ModelEditPlan{}, err
	}
	maintainer.mu.Lock()
	maintainer.calls++
	maintainer.mu.Unlock()
	ref := app.SourceRefKey(input.Source)
	switch {
	case input.Source.Source.ID == decisionOrigin && input.Source.Version.Version == decisionVersionOne:
		refs := []string{ref}
		return projectPlan(input, refs, domain.FileEdit{Path: testDecisionPath, Content: []byte(projectDecision(refs, 1))}), nil
	case input.Source.Source.ID == "investigation-session-recovery" && input.Source.Version.Version == "1":
		return updateProjectDecision(input, ref, 2)
	case input.Source.Source.ID == decisionOrigin && input.Source.Version.Version == decisionVersionTwo:
		return updateProjectDecision(input, ref, 3)
	case input.Source.Source.ID == "runbook-session-recovery" && input.Source.Version.Version == "1":
		refs := []string{ref}
		return projectPlan(input, refs, domain.FileEdit{Path: testRunbookPath, Content: []byte(projectRunbook(refs))}), nil
	default:
		return domain.ModelEditPlan{}, fmt.Errorf("unexpected project source %s@%s", input.Source.Source.ID, input.Source.Version.Version)
	}
}

func (maintainer *projectMaintainer) Calls() int {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	return maintainer.calls
}

func updateProjectDecision(input domain.MaintenanceInput, ref string, stage int) (domain.ModelEditPlan, error) {
	var existing *domain.PageSnapshot
	for index := range input.Pages {
		if input.Pages[index].ID == testDecisionID {
			existing = &input.Pages[index]
			break
		}
	}
	if existing == nil || existing.Digest == "" {
		return domain.ModelEditPlan{}, fmt.Errorf("session-memory decision missing from maintenance context")
	}
	refs := append(append([]string(nil), existing.SourceRefs...), ref)
	slices.Sort(refs)
	refs = slices.Compact(refs)
	return projectPlan(input, refs, domain.FileEdit{
		Path: testDecisionPath, ExpectedDigest: existing.Digest, Content: []byte(projectDecision(refs, stage)),
	}), nil
}

func projectPlan(input domain.MaintenanceInput, refs []string, edit domain.FileEdit) domain.ModelEditPlan {
	edits := []domain.FileEdit{edit}
	for _, catalog := range input.Catalogs {
		if catalog.Path != "wiki/index.md" {
			continue
		}
		target := strings.TrimPrefix(edit.Path, "wiki/")
		edits = append(edits, domain.FileEdit{
			Path: catalog.Path, ExpectedDigest: catalog.Digest,
			Content: []byte(strings.TrimRight(catalog.Content, "\n") + "\n\n* [Knowledge](" + target + ")\n"),
		})
		break
	}
	return domain.ModelEditPlan{
		SchemaDigest: input.Schema.Digest, SourceRefs: append([]string(nil), refs...), Edits: edits,
		Rationale: "maintain the project-decisions example",
	}
}

func projectDecision(refs []string, stage int) string {
	body := "Badger was selected for durable session memory because it offered bounded local persistence.\n"
	if stage >= 2 {
		body += "\nThe crash recovery investigation confirmed durable replay, operation lease recovery, and projection rebuilds.\n"
	}
	if stage >= 3 {
		body += "\nSQLite replaced and superseded Badger for the active implementation. The historical Badger rationale remains inspectable.\n"
	}
	return projectFrontmatter(testDecisionID, "Session Memory Decision", "decision", refs) + "# Session Memory Decision\n\n" + body
}

func projectRunbook(refs []string) string {
	return projectFrontmatter(testRunbookID, "Session Recovery Runbook", "runbook", refs) +
		"# Session Recovery Runbook\n\nUse [[decisions/session-memory]] before recovery.\n\n" +
		"1. Restart the sidecar.\n2. Poll the durable operation.\n3. Verify its recovery lease.\n4. Rebuild projections when required.\n"
}

func projectFrontmatter(id domain.PageID, title, pageType string, refs []string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "---\nid: %s\ntitle: %s\ntype: %s\nsource_refs:\n", id, title, pageType)
	for _, ref := range refs {
		fmt.Fprintf(&builder, "  - %s\n", ref)
	}
	builder.WriteString("---\n")
	return builder.String()
}
