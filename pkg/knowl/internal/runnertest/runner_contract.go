// Package runnertest provides the backend-neutral durable runner contract.
package runnertest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

// Run exercises the same claimed application workflow against an operational
// store and its rebuildable search projection.
func Run(t *testing.T, operations app.OperationStore, index app.SearchIndex, scope domain.ScopeRef) {
	t.Helper()
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new runner workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init runner workspace: %v", err)
	}
	schema, err := workspace.Schema(ctx, scope)
	if err != nil {
		t.Fatalf("read runner schema: %v", err)
	}
	maintainer := &contractMaintainer{plan: domain.ModelEditPlan{
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{"fixture:runner@1"},
		Edits: []domain.FileEdit{{
			Path:    "wiki/entities/runner.md",
			Content: contractPage("entities/runner", "Runner", "fixture:runner@1"),
		}},
	}}
	service, err := app.NewIngestService(workspace, operations, index, maintainer, app.IngestOptions{LeaseDuration: time.Nanosecond})
	if err != nil {
		t.Fatalf("new runner service: %v", err)
	}

	first := contractEnvelope(scope, "runner")
	submission, err := service.Submit(ctx, first)
	if err != nil {
		t.Fatalf("submit runner source: %v", err)
	}
	claim, err := operations.ClaimReady(ctx, scope, contractLease("runner-owner"))
	if err != nil {
		t.Fatalf("claim runner source: %v", err)
	}
	result, err := service.RunToTerminal(ctx, claim)
	if err != nil {
		t.Fatalf("run source to terminal: %v", err)
	}
	if result.Operation.Status != domain.StatusCommitted || result.Commit == nil {
		t.Fatalf("runner result = %#v, want committed", result)
	}
	generation := result.Commit.Generation
	replay, err := service.Submit(ctx, first)
	if err != nil {
		t.Fatalf("replay runner source: %v", err)
	}
	if replay.Operation.ID != submission.Operation.ID || replay.Operation.Status != domain.StatusCommitted {
		t.Fatalf("runner replay = %#v, want same committed operation", replay.Operation)
	}
	if maintainer.Calls() != 1 {
		t.Fatalf("maintainer calls after terminal replay = %d, want 1", maintainer.Calls())
	}

	second := contractEnvelope(scope, "adopted")
	adopted, err := service.Submit(ctx, second)
	if err != nil {
		t.Fatalf("submit adopted source: %v", err)
	}
	staged, err := workspace.StagePlan(ctx, domain.ValidatedEditPlan{
		OperationID:  string(adopted.Operation.ID),
		Scope:        scope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{"fixture:adopted@1"},
		Edits: []domain.FileEdit{{
			Path:    "wiki/entities/adopted.md",
			Content: contractPage("entities/adopted", "Adopted", "fixture:adopted@1"),
		}},
	})
	if err != nil {
		t.Fatalf("persist adopted stage: %v", err)
	}
	claim, err = operations.ClaimReady(ctx, scope, contractLease("adopted-owner"))
	if err != nil {
		t.Fatalf("claim adopted source: %v", err)
	}
	result, err = service.RunToTerminal(ctx, claim)
	if err != nil {
		t.Fatalf("run adopted source: %v", err)
	}
	if result.Operation.Status != domain.StatusCommitted || result.Staged.Digest != staged.Digest {
		t.Fatalf("adopted result = %#v, want committed staged digest", result)
	}
	if maintainer.Calls() != 1 {
		t.Fatalf("post-stage replay invoked maintainer; calls = %d", maintainer.Calls())
	}

	results, err := index.Search(ctx, scope, "Runner", domain.ReadLimits{Pages: 5}, nil)
	if err != nil {
		t.Fatalf("search runner projection: %v", err)
	}
	if len(results) == 0 || results[0].ID != "entities/runner" {
		t.Fatalf("runner search results = %#v", results)
	}
	logContent, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("read runner log: %v", err)
	}
	if count := bytes.Count(logContent, []byte(submission.Operation.ID)); count != 1 {
		t.Fatalf("operation log entries = %d, want one for generation %q", count, generation)
	}
}

type contractMaintainer struct {
	mu      sync.Mutex
	plan    domain.ModelEditPlan
	counter int
}

func (maintainer *contractMaintainer) Plan(context.Context, domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	maintainer.counter++
	return maintainer.plan, nil
}

func (maintainer *contractMaintainer) Calls() int {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	return maintainer.counter
}

func contractEnvelope(scope domain.ScopeRef, id string) domain.SourceEnvelope {
	content := []byte("durable " + id)
	digest := sha256.Sum256(content)
	return domain.SourceEnvelope{
		Scope: scope, Source: domain.SourceRef{Adapter: "fixture", ID: id},
		Version:   domain.SourceVersion{Version: "1", Digest: fmt.Sprintf("%x", digest)},
		MediaType: "text/plain", Content: content,
	}
}

func contractLease(token string) domain.WorkLease {
	return domain.WorkLease{Token: token, ExpiresAt: time.Now().Add(time.Minute)}
}

func contractPage(id, title, sourceRef string) []byte {
	return []byte(fmt.Sprintf("---\nid: %s\ntitle: %s\ntype: entity\nsource_refs:\n  - %s\n---\n# %s\n", id, title, sourceRef, title))
}
