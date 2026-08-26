package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestProviderFreeIngestFailsBeforeDurableMutation(t *testing.T) {
	ctx := context.Background()
	workspace, store, _, _ := newWorkflow(t, false, nil)
	service, err := app.NewIngestService(workspace, store, store, nil, app.IngestOptions{})
	if err != nil {
		t.Fatalf("new provider-free ingest service: %v", err)
	}
	envelope := sourceEnvelope([]byte("secret provider-free source"))

	tests := []struct {
		name string
		call func() error
	}{
		{name: "submit", call: func() error { _, callErr := service.Submit(ctx, envelope); return callErr }},
		{name: "execute", call: func() error { _, callErr := service.Execute(ctx, app.IngestSubmission{}); return callErr }},
		{name: "run to terminal", call: func() error { _, callErr := service.RunToTerminal(ctx, knowl.WorkClaim{}); return callErr }},
		{name: "ingest", call: func() error { _, callErr := service.Ingest(ctx, envelope); return callErr }},
		{name: "preview", call: func() error { _, callErr := service.Preview(ctx, envelope); return callErr }},
		{name: "file plan", call: func() error { _, callErr := service.FilePlan(ctx, envelope, knowl.ModelEditPlan{}); return callErr }},
		{name: "apply", call: func() error { _, callErr := service.Apply(ctx, envelope.Scope, "operation"); return callErr }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if callErr := test.call(); !errors.Is(callErr, app.ErrMaintainerUnavailable) {
				t.Fatalf("error = %v, want maintainer unavailable", callErr)
			}
		})
	}

	inspection, err := workspace.Inspect(ctx, envelope.Scope)
	if err != nil {
		t.Fatalf("inspect provider-free workspace: %v", err)
	}
	if len(inspection.RawSources) != 0 {
		t.Fatalf("provider-free ingest accepted raw sources: %#v", inspection.RawSources)
	}
	ready, err := store.ResumeReady(ctx, envelope.Scope, 10)
	if err != nil {
		t.Fatalf("read provider-free operations: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("provider-free ingest reserved operations: %#v", ready)
	}
	if _, err := service.Recover(ctx); err != nil {
		t.Fatalf("provider-free recovery: %v", err)
	}
}

func TestIngestReviewApplyReplayAndProject(t *testing.T) {
	ctx := context.Background()
	workspace, store, service, maintainer := newWorkflow(t, false, nil)
	content := []byte("source text")
	envelope := sourceEnvelope(content)

	planned, err := service.Ingest(ctx, envelope)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if planned.Operation.Status != knowl.StatusAwaitingReview {
		t.Fatalf("planned status = %q, want awaiting_review", planned.Operation.Status)
	}
	if planned.Commit != nil {
		t.Fatal("review-only ingest committed canonical content")
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), "wiki", "entities", "one.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("planned page stat = %v, want absent", err)
	}
	if maintainer.calls() != 1 {
		t.Fatalf("maintainer calls after initial ingest = %d, want 1", maintainer.calls())
	}
	if _, err := workspace.LoadStage(ctx, envelope.Scope, planned.Operation.ID); err != nil {
		t.Fatalf("load durable review stage: %v", err)
	}

	applied, err := service.Apply(ctx, envelope.Scope, planned.Operation.ID)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.Operation.Status != knowl.StatusCommitted {
		t.Fatalf("applied status = %q, want committed", applied.Operation.Status)
	}
	if applied.Commit == nil || len(applied.Commit.Files) != 4 {
		t.Fatalf("commit = %#v, want two pages, root catalog, and log", applied.Commit)
	}
	page, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "entities", "one.md"))
	if err != nil {
		t.Fatalf("read committed page: %v", err)
	}
	if string(page) != string(planPageContent) {
		t.Fatalf("committed page = %q", page)
	}
	logContent, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("read commit log: %v", err)
	}
	if !contains(string(logContent), strings.ReplaceAll(string(planned.Operation.ID), "-", `\u002d`)) {
		t.Fatalf("commit log does not cite operation: %q", logContent)
	}
	results, err := store.Search(ctx, envelope.Scope, "One", knowl.ReadLimits{Pages: 5}, nil)
	if err != nil {
		t.Fatalf("search projected page: %v", err)
	}
	if len(results) != 1 || results[0].ID != testPageID {
		t.Fatalf("search results = %#v", results)
	}
	if len(results[0].SourceRefs) != 1 || results[0].SourceRefs[0] != testSourceRef {
		t.Fatalf("search source refs = %#v", results[0].SourceRefs)
	}
	links, err := store.Links(ctx, envelope.Scope, testPageID, knowl.ReadLimits{Pages: 5})
	if err != nil {
		t.Fatalf("read projected links: %v", err)
	}
	if len(links) != 1 || links[0].To != "entities/two" {
		t.Fatalf("projected links = %#v", links)
	}

	replay, err := service.Ingest(ctx, envelope)
	if err != nil {
		t.Fatalf("replay ingest: %v", err)
	}
	if replay.Operation.Status != knowl.StatusCommitted {
		t.Fatalf("replay status = %q, want committed", replay.Operation.Status)
	}
	if maintainer.calls() != 1 {
		t.Fatalf("maintainer calls after replay = %d, want 1", maintainer.calls())
	}
	secondLog, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("read replay log: %v", err)
	}
	if string(secondLog) != string(logContent) {
		t.Fatal("replay changed the canonical log")
	}
}

func TestSubmitReplayUsesDurableExecutionDescriptor(t *testing.T) {
	ctx := context.Background()
	workspace, store, service, maintainer := newWorkflow(t, false, nil, func(schema knowl.SchemaDocument) knowl.ModelEditPlan {
		return knowl.ModelEditPlan{
			SchemaDigest: schema.Digest,
			SourceRefs:   []string{testSourceRef},
			Edits:        []knowl.FileEdit{},
			Rationale:    "verify durable schema snapshot",
		}
	})
	envelope := sourceEnvelope([]byte("durable source"))
	first, err := service.Submit(ctx, envelope)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	descriptor, err := store.Execution(ctx, envelope.Scope, first.Operation.ID)
	if err != nil {
		t.Fatalf("read execution descriptor: %v", err)
	}
	if descriptor.Source.Source != envelope.Source || descriptor.Source.Version != envelope.Version {
		t.Fatalf("stored source = %#v, want envelope identity", descriptor.Source)
	}

	schemaPath := filepath.Join(workspace.Root(), "schema.md")
	currentSchema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema fixture: %v", err)
	}
	if err := os.WriteFile(schemaPath, append(currentSchema, []byte("\nReplay policy change.\n")...), 0o600); err != nil {
		t.Fatalf("change schema fixture: %v", err)
	}
	replay, err := service.Submit(ctx, envelope)
	if err != nil {
		t.Fatalf("replay submit: %v", err)
	}
	if replay.NeedsExecution() {
		t.Fatal("replay unexpectedly reported newly created work")
	}
	if _, err := service.Execute(ctx, replay); !errors.Is(err, contentfs.ErrPrecondition) {
		t.Fatalf("execute replay error = %v, want stale durable schema precondition", err)
	}
	if got := maintainer.schemaDigest(); got != descriptor.Schema.Digest {
		t.Fatalf("maintainer schema digest = %q, want durable %q", got, descriptor.Schema.Digest)
	}
}

func TestIngestAcceptsAndCommitsNoOpPlan(t *testing.T) {
	ctx := context.Background()
	workspace, _, service, _ := newWorkflow(t, false, nil, func(schema knowl.SchemaDocument) knowl.ModelEditPlan {
		return knowl.ModelEditPlan{
			SchemaDigest: schema.Digest,
			SourceRefs:   []string{testSourceRef},
			Edits:        []knowl.FileEdit{},
			Rationale:    "source requires no canonical changes",
		}
	})
	planned, err := service.Ingest(ctx, sourceEnvelope([]byte("source text")))
	if err != nil {
		t.Fatalf("ingest no-op plan: %v", err)
	}
	if planned.Operation.Status != knowl.StatusAwaitingReview || len(planned.Plan.Edits) != 0 {
		t.Fatalf("planned no-op = %#v", planned)
	}
	applied, err := service.Apply(ctx, "local", planned.Operation.ID)
	if err != nil {
		t.Fatalf("apply no-op plan: %v", err)
	}
	if applied.Operation.Status != knowl.StatusCommitted || applied.Commit == nil {
		t.Fatalf("applied no-op = %#v", applied)
	}
	if len(applied.Commit.Files) != 1 || applied.Commit.Files[0] != "wiki/log.md" {
		t.Fatalf("no-op commit files = %#v, want provenance log", applied.Commit.Files)
	}
	logContent, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("read no-op commit log: %v", err)
	}
	if !contains(string(logContent), strings.ReplaceAll(string(planned.Operation.ID), "-", `\u002d`)) {
		t.Fatalf("no-op commit log does not cite operation: %q", logContent)
	}
}

func TestIngestCommitsIndexAlongsidePagesAndLog(t *testing.T) {
	ctx := context.Background()
	workspace, _, service, maintainer := newWorkflow(t, false, nil)
	indexPath := filepath.Join(workspace.Root(), "wiki", "index.md")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	maintainer.mu.Lock()
	maintainer.plan.Edits = []knowl.FileEdit{
		{Path: testPagePath, Content: planPageContent},
		{Path: testPageTwoPath, Content: planSupportingContent},
		{Path: testRootCatalogPath, ExpectedDigest: digest(indexBefore), Content: append(indexBefore, []byte("\n* [One](entities/one.md)\n* [Two](entities/two.md)\n")...)},
	}
	maintainer.mu.Unlock()
	planned, err := service.Ingest(ctx, sourceEnvelope([]byte("source text")))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	result, err := service.Apply(ctx, "local", planned.Operation.ID)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Operation.Status != knowl.StatusCommitted || len(result.Commit.Files) != 4 {
		t.Fatalf("index commit result = %#v, want two pages, index, and log", result)
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read updated index: %v", err)
	}
	if string(indexAfter) != string(append(indexBefore, []byte("\n* [One](entities/one.md)\n* [Two](entities/two.md)\n")...)) {
		t.Fatalf("updated index = %q", indexAfter)
	}
}

func TestConcurrentReviewReplayConvergesToOneOperation(t *testing.T) {
	ctx := context.Background()
	_, store, service, maintainer := newWorkflow(t, false, nil)
	envelope := sourceEnvelope([]byte("source text"))
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, ingestErr := service.Ingest(ctx, envelope)
			results <- ingestErr
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent ingest: %v", err)
		}
	}
	operationID := knowl.OperationID("local:fixture:source-1@1#" + digest([]byte("source text"))[:16])
	operation, err := store.Operation(ctx, "local", operationID)
	if err != nil {
		t.Fatalf("read converged operation: %v", err)
	}
	if operation.Status != knowl.StatusAwaitingReview {
		t.Fatalf("converged operation status = %q", operation.Status)
	}
	if maintainer.calls() < 1 {
		t.Fatal("concurrent ingest never invoked maintainer")
	}
}

func TestIngestRejectsStaleReviewedPlan(t *testing.T) {
	ctx := context.Background()
	before := []byte("---\nid: entities/stale\ntitle: Stale\ntype: entity\nsource_refs:\n  - " + testSourceRef + "\n---\n# Stale\n\nbefore\n")
	workspace, store, service, _ := newWorkflow(t, false, nil, func(schema knowl.SchemaDocument) knowl.ModelEditPlan {
		return knowl.ModelEditPlan{
			SchemaDigest: schema.Digest,
			SourceRefs:   []string{testSourceRef},
			Edits:        []knowl.FileEdit{{Path: "wiki/entities/stale.md", ExpectedDigest: digest(before), Content: []byte("---\nid: entities/stale\ntitle: Stale\ntype: entity\nsource_refs:\n  - " + testSourceRef + "\n---\n# Stale\n\nafter\n")}},
		}
	})
	if err := os.WriteFile(filepath.Join(workspace.Root(), "wiki", "entities", "stale.md"), before, 0o600); err != nil {
		t.Fatalf("write stale fixture: %v", err)
	}
	planned, err := service.Ingest(ctx, sourceEnvelope([]byte("source text")))
	if err != nil {
		t.Fatalf("ingest stale plan: %v", err)
	}
	humanEdit := []byte("---\nid: entities/stale\ntitle: Human edit\ntype: entity\nsource_refs:\n  - " + testSourceRef + "\n---\n# Human edit\n")
	if err := os.WriteFile(filepath.Join(workspace.Root(), "wiki", "entities", "stale.md"), humanEdit, 0o600); err != nil {
		t.Fatalf("write human edit: %v", err)
	}
	_, err = service.Apply(ctx, "local", planned.Operation.ID)
	if !errors.Is(err, contentfs.ErrPrecondition) {
		t.Fatalf("stale apply error = %v, want precondition", err)
	}
	operation, err := store.Operation(ctx, "local", planned.Operation.ID)
	if err != nil {
		t.Fatalf("read failed operation: %v", err)
	}
	if operation.Status != knowl.StatusFailed {
		t.Fatalf("stale operation status = %q, want failed", operation.Status)
	}
	content, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "entities", "stale.md"))
	if err != nil {
		t.Fatalf("read human edit: %v", err)
	}
	if string(content) != string(humanEdit) {
		t.Fatalf("human edit was overwritten: %q", content)
	}
}

func TestIngestRejectsStaleSchemaAtApply(t *testing.T) {
	ctx := context.Background()
	workspace, store, service, _ := newWorkflow(t, false, nil)
	planned, err := service.Ingest(ctx, sourceEnvelope([]byte("source text")))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	schemaPath := filepath.Join(workspace.Root(), "schema.md")
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if err := os.WriteFile(schemaPath, append(schema, []byte("\noperator change\n")...), 0o600); err != nil {
		t.Fatalf("change schema: %v", err)
	}
	_, err = service.Apply(ctx, "local", planned.Operation.ID)
	if !errors.Is(err, contentfs.ErrPrecondition) {
		t.Fatalf("stale schema error = %v, want precondition", err)
	}
	operation, err := store.Operation(ctx, "local", planned.Operation.ID)
	if err != nil {
		t.Fatalf("read stale schema operation: %v", err)
	}
	if operation.Status != knowl.StatusFailed {
		t.Fatalf("stale schema status = %q, want failed", operation.Status)
	}
}

func TestProjectionFailureLeavesCanonicalCommitRetryable(t *testing.T) {
	ctx := context.Background()
	workspace, store, service, _ := newWorkflow(t, false, failingIndex{})
	planned, err := service.Ingest(ctx, sourceEnvelope([]byte("source text")))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	result, err := service.Apply(ctx, "local", planned.Operation.ID)
	if !errors.Is(err, app.ErrProjection) {
		t.Fatalf("projection error = %v, want projection error", err)
	}
	operation, err := store.Operation(ctx, "local", planned.Operation.ID)
	if err != nil {
		t.Fatalf("read committed operation: %v", err)
	}
	if operation.Status != knowl.StatusApplying || operation.Failure != nil {
		t.Fatalf("projection failure operation = %#v, want retryable applying", operation)
	}
	if result.Commit == nil {
		t.Fatal("projection failure did not report the canonical commit")
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), "wiki", "entities", "one.md")); err != nil {
		t.Fatalf("canonical page missing after projection failure: %v", err)
	}
}

func TestRunToTerminalPlansAndCommitsClaimedOperation(t *testing.T) {
	ctx := context.Background()
	_, store, service, maintainer := newWorkflow(t, false, nil)
	submission, err := service.Submit(ctx, sourceEnvelope([]byte("runner source")))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	claim := claimReady(t, store, submission.Operation.Key.Scope)

	result, err := service.RunToTerminal(ctx, claim)
	if err != nil {
		t.Fatalf("run to terminal: %v", err)
	}
	if result.Operation.Status != knowl.StatusCommitted || result.Commit == nil {
		t.Fatalf("runner result = %#v, want committed", result)
	}
	if maintainer.calls() != 1 {
		t.Fatalf("maintainer calls = %d, want 1", maintainer.calls())
	}
}

func TestRunToTerminalAdoptsDurableStageWithoutReplanning(t *testing.T) {
	ctx := context.Background()
	workspace, store, service, maintainer := newWorkflow(t, false, nil)
	submission, err := service.Submit(ctx, sourceEnvelope([]byte("staged source")))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	schema, err := workspace.Schema(ctx, submission.Operation.Key.Scope)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	inspection, err := workspace.Inspect(ctx, submission.Operation.Key.Scope)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	staged, err := workspace.StagePlan(ctx, knowl.ValidatedEditPlan{
		OperationID:  string(submission.Operation.ID),
		Scope:        submission.Operation.Key.Scope,
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testSourceRef},
		Edits: []knowl.FileEdit{
			{Path: testPagePath, Content: planPageContent},
			{Path: testPageTwoPath, Content: planSupportingContent},
			{Path: testRootCatalogPath, ExpectedDigest: inspection.Index.Digest, Content: []byte(inspection.Index.Content + "\n* [One](entities/one.md)\n* [Two](entities/two.md)\n")},
		},
	})
	if err != nil {
		t.Fatalf("stage plan: %v", err)
	}
	restartedWorkspace, err := contentfs.New(workspace.Root())
	if err != nil {
		t.Fatalf("reopen workspace: %v", err)
	}
	restartedService, err := app.NewIngestService(restartedWorkspace, store, store, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatalf("recreate ingest service: %v", err)
	}
	result, err := restartedService.RunToTerminal(ctx, claimReady(t, store, submission.Operation.Key.Scope))
	if err != nil {
		t.Fatalf("run staged operation: %v", err)
	}
	if result.Operation.Status != knowl.StatusCommitted || result.Staged.Digest != staged.Digest {
		t.Fatalf("runner result = %#v, want adopted stage %q", result, staged.Digest)
	}
	if maintainer.calls() != 0 {
		t.Fatalf("maintainer calls = %d, want no replanning", maintainer.calls())
	}
}

func TestRunToTerminalFailsPlannedOperationWithoutStage(t *testing.T) {
	ctx := context.Background()
	_, store, service, maintainer := newWorkflow(t, false, nil)
	submission, err := service.Submit(ctx, sourceEnvelope([]byte("missing stage")))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := store.SavePlan(ctx, submission.Operation.ID, knowl.PlanSummary{
		OperationID: string(submission.Operation.ID), Digest: "missing-stage", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save plan state: %v", err)
	}
	result, err := service.RunToTerminal(ctx, claimReady(t, store, submission.Operation.Key.Scope))
	if !errors.Is(err, app.ErrStageNotFound) {
		t.Fatalf("run without stage error = %v, want stage not found", err)
	}
	if result.Operation.Status != knowl.StatusFailed || result.Operation.Failure == nil || result.Operation.Failure.Class != "staging" {
		t.Fatalf("runner result = %#v, want failed staging", result)
	}
	if maintainer.calls() != 0 {
		t.Fatalf("maintainer calls = %d, want 0", maintainer.calls())
	}
}

func TestRunToTerminalRetriesAfterCanonicalProjectionFailure(t *testing.T) {
	ctx := context.Background()
	index := &failOnceProjectionIndex{}
	workspace, store, service, maintainer := newWorkflowWithOptions(t, app.IngestOptions{LeaseDuration: time.Nanosecond}, index)
	submission, err := service.Submit(ctx, sourceEnvelope([]byte("retry projection")))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	claim := claimReady(t, store, submission.Operation.Key.Scope)
	first, err := service.RunToTerminal(ctx, claim)
	if !errors.Is(err, app.ErrProjection) {
		t.Fatalf("first run error = %v, want projection failure", err)
	}
	if first.Operation.Status != knowl.StatusApplying || first.Commit == nil {
		t.Fatalf("first result = %#v, want retryable canonical commit", first)
	}
	logBeforeRetry, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("read log before retry: %v", err)
	}
	restartedWorkspace, err := contentfs.New(workspace.Root())
	if err != nil {
		t.Fatalf("reopen workspace: %v", err)
	}
	restartedService, err := app.NewIngestService(restartedWorkspace, store, index, maintainer, app.IngestOptions{LeaseDuration: time.Nanosecond})
	if err != nil {
		t.Fatalf("recreate ingest service: %v", err)
	}
	second, err := restartedService.RunToTerminal(ctx, claim)
	if err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if second.Operation.Status != knowl.StatusCommitted || second.Commit == nil || second.Commit.Generation != first.Commit.Generation {
		t.Fatalf("retry result = %#v, want same committed generation", second)
	}
	logAfterRetry, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "log.md"))
	if err != nil {
		t.Fatalf("read log after retry: %v", err)
	}
	if string(logAfterRetry) != string(logBeforeRetry) {
		t.Fatal("retry duplicated the canonical provenance log entry")
	}
	if maintainer.calls() != 1 {
		t.Fatalf("maintainer calls = %d, want stage reuse", maintainer.calls())
	}
}

func TestRunToTerminalRetriesAfterCommitOutcomeFailure(t *testing.T) {
	ctx := context.Background()
	workspace, store, _, maintainer := newWorkflow(t, false, nil)
	operations := &failOnceOutcomeStore{OperationStore: store}
	service, err := app.NewIngestService(workspace, operations, store, maintainer, app.IngestOptions{LeaseDuration: time.Nanosecond})
	if err != nil {
		t.Fatalf("new ingest service: %v", err)
	}
	submission, err := service.Submit(ctx, sourceEnvelope([]byte("retry outcome")))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	claim := claimReady(t, store, submission.Operation.Key.Scope)
	first, err := service.RunToTerminal(ctx, claim)
	if !errors.Is(err, errOutcomeUnavailable) {
		t.Fatalf("first run error = %v, want outcome failure", err)
	}
	if first.Operation.Status != knowl.StatusApplying || first.Commit == nil {
		t.Fatalf("first result = %#v, want retryable canonical commit", first)
	}
	restartedWorkspace, err := contentfs.New(workspace.Root())
	if err != nil {
		t.Fatalf("reopen workspace: %v", err)
	}
	restartedService, err := app.NewIngestService(restartedWorkspace, operations, store, maintainer, app.IngestOptions{LeaseDuration: time.Nanosecond})
	if err != nil {
		t.Fatalf("recreate ingest service: %v", err)
	}
	second, err := restartedService.RunToTerminal(ctx, claim)
	if err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if second.Operation.Status != knowl.StatusCommitted || second.Commit == nil || second.Commit.Generation != first.Commit.Generation {
		t.Fatalf("retry result = %#v, want same committed generation", second)
	}
}

func TestRunToTerminalKeepsActiveApplyLeaseRetryable(t *testing.T) {
	ctx := context.Background()
	_, store, service, _ := newWorkflow(t, false, nil)
	planned, err := service.Ingest(ctx, sourceEnvelope([]byte("leased operation")))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := store.MarkApplying(ctx, planned.Operation.ID, knowl.Lease{
		Token: "active-apply", ExpiresAt: time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatalf("mark applying: %v", err)
	}
	result, err := service.RunToTerminal(ctx, claimReady(t, store, planned.Operation.Key.Scope))
	if !errors.Is(err, app.ErrApplyLeaseConflict) {
		t.Fatalf("runner error = %v, want apply lease conflict", err)
	}
	operation, readErr := store.Operation(ctx, planned.Operation.Key.Scope, planned.Operation.ID)
	if readErr != nil {
		t.Fatalf("read operation: %v", readErr)
	}
	if result.Operation.Status != knowl.StatusApplying || operation.Status != knowl.StatusApplying || operation.Failure != nil {
		t.Fatalf("leased operation = %#v / %#v, want retryable applying", result.Operation, operation)
	}
}

func TestRunToTerminalResumesExpiredApplyingAfterServiceRestart(t *testing.T) {
	ctx := context.Background()
	workspace, store, service, maintainer := newWorkflow(t, false, nil)
	planned, err := service.Ingest(ctx, sourceEnvelope([]byte("expired applying")))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := store.MarkApplying(ctx, planned.Operation.ID, knowl.Lease{
		Token: "crashed-owner", ExpiresAt: time.Unix(1, 0).UTC(),
	}); err != nil {
		t.Fatalf("persist crashed applying state: %v", err)
	}
	restartedWorkspace, err := contentfs.New(workspace.Root())
	if err != nil {
		t.Fatalf("reopen workspace: %v", err)
	}
	restartedService, err := app.NewIngestService(restartedWorkspace, store, store, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatalf("recreate ingest service: %v", err)
	}
	result, err := restartedService.RunToTerminal(ctx, claimReady(t, store, planned.Operation.Key.Scope))
	if err != nil {
		t.Fatalf("resume applying operation: %v", err)
	}
	if result.Operation.Status != knowl.StatusCommitted || result.Commit == nil {
		t.Fatalf("resumed applying result = %#v, want committed", result)
	}
	if maintainer.calls() != 1 {
		t.Fatalf("resumed applying maintainer calls = %d, want no replay", maintainer.calls())
	}
}

func TestRunToTerminalCancellationDoesNotFailOperation(t *testing.T) {
	ctx := context.Background()
	_, store, service, _ := newWorkflow(t, false, nil)
	submission, err := service.Submit(ctx, sourceEnvelope([]byte("cancelled operation")))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	claim := claimReady(t, store, submission.Operation.Key.Scope)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := service.RunToTerminal(cancelled, claim); !errors.Is(err, context.Canceled) {
		t.Fatalf("runner error = %v, want context cancellation", err)
	}
	operation, err := store.Operation(ctx, submission.Operation.Key.Scope, submission.Operation.ID)
	if err != nil {
		t.Fatalf("read operation: %v", err)
	}
	if operation.Status != knowl.StatusReceived || operation.Failure != nil {
		t.Fatalf("cancelled operation = %#v, want retryable received", operation)
	}
}

func TestAutoApplyIsExplicit(t *testing.T) {
	ctx := context.Background()
	workspace, store, service, maintainer := newWorkflow(t, true, nil)
	_ = workspace
	_ = store
	_ = maintainer
	result, err := service.Ingest(ctx, sourceEnvelope([]byte("source text")))
	if err != nil {
		t.Fatalf("auto apply ingest: %v", err)
	}
	if result.Operation.Status != knowl.StatusCommitted || result.Commit == nil {
		t.Fatalf("auto apply result = %#v, want committed result", result)
	}
}

func TestSubmitReservesWithoutPlanningAndMarksReplay(t *testing.T) {
	ctx := context.Background()
	_, _, service, maintainer := newWorkflow(t, false, nil)
	envelope := sourceEnvelope([]byte("source text"))

	first, err := service.Submit(ctx, envelope)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !first.NeedsExecution() || first.Operation.Status != knowl.StatusReceived {
		t.Fatalf("first submission = %#v, want new received operation", first.Operation)
	}
	if maintainer.calls() != 0 {
		t.Fatalf("maintainer calls after submit = %d, want 0", maintainer.calls())
	}

	second, err := service.Submit(ctx, envelope)
	if err != nil {
		t.Fatalf("replay submit: %v", err)
	}
	if second.NeedsExecution() || second.Operation.ID != first.Operation.ID {
		t.Fatalf("replayed submission = %#v, want existing operation", second.Operation)
	}
}

func TestReserveAcceptedUsesExistingRawAndDurableOperationIdentity(t *testing.T) {
	ctx := context.Background()
	workspace, store, publicService, maintainer := newWorkflow(t, false, nil)
	envelope := sourceEnvelope([]byte("configured source text"))
	accepted, err := workspace.AcceptSource(ctx, envelope)
	if err != nil {
		t.Fatalf("accept legacy raw source: %v", err)
	}
	document := knowl.SourceDocument{
		SourceID: testConfiguredSourceID, DocumentID: "docs/Привет.md", Revision: envelope.Version.Version,
		URI: "file:///srv/wiki/docs/%D0%9F%D1%80%D0%B8%D0%B2%D0%B5%D1%82.md",
	}
	content := &rejectingAcceptStore{Workspace: workspace}
	queue, err := app.NewIngestService(content, store, store, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatalf("new accepted-source queue: %v", err)
	}
	request := app.AcceptedMaintenanceRequest{Source: accepted, SourceDocument: document, ContentType: accepted.MediaType}

	first, err := queue.ReserveAccepted(ctx, request)
	if err != nil {
		t.Fatalf("reserve accepted source: %v", err)
	}
	if first.OperationID == "" || first.Replayed {
		t.Fatalf("first reservation = %#v, want new operation", first)
	}
	if content.acceptCalls != 0 {
		t.Fatalf("accepted reservation called AcceptSource %d times", content.acceptCalls)
	}
	descriptor, err := store.Execution(ctx, accepted.Scope, first.OperationID)
	if err != nil {
		t.Fatalf("read accepted execution descriptor: %v", err)
	}
	if descriptor.Source.SourceDocument != document {
		t.Fatalf("durable source document = %#v, want %#v", descriptor.Source.SourceDocument, document)
	}

	replay, err := queue.ReserveAccepted(ctx, request)
	if err != nil {
		t.Fatalf("replay accepted source: %v", err)
	}
	if !replay.Replayed || replay.OperationID != first.OperationID {
		t.Fatalf("replay reservation = %#v, want operation %q", replay, first.OperationID)
	}
	publicReplay, err := publicService.Submit(ctx, envelope)
	if err != nil {
		t.Fatalf("public submit identity replay: %v", err)
	}
	if publicReplay.Operation.ID != first.OperationID {
		t.Fatalf("public operation = %q, accepted operation = %q", publicReplay.Operation.ID, first.OperationID)
	}
	inspection, err := workspace.Inspect(ctx, accepted.Scope)
	if err != nil {
		t.Fatalf("inspect raw sources: %v", err)
	}
	if len(inspection.RawSources) != 1 {
		t.Fatalf("raw sources = %d, want exactly one immutable revision", len(inspection.RawSources))
	}
}

func TestReserveAcceptedRejectsInvalidOrBinaryRequestsBeforeReservation(t *testing.T) {
	ctx := context.Background()
	workspace, store, _, maintainer := newWorkflow(t, false, nil)
	envelope := sourceEnvelope([]byte("configured source text"))
	accepted, err := workspace.AcceptSource(ctx, envelope)
	if err != nil {
		t.Fatalf("accept raw source: %v", err)
	}
	content := &rejectingAcceptStore{Workspace: workspace}
	queue, err := app.NewIngestService(content, store, store, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatalf("new accepted-source queue: %v", err)
	}
	document := knowl.SourceDocument{SourceID: testConfiguredSourceID, DocumentID: "docs/page.md", Revision: "wrong", URI: "file:///srv/wiki/docs/page.md"}
	if _, err := queue.ReserveAccepted(ctx, app.AcceptedMaintenanceRequest{Source: accepted, SourceDocument: document, ContentType: accepted.MediaType}); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("mismatched revision error = %v, want source invalid", err)
	}
	document.Revision = accepted.Version.Version
	if _, err := queue.ReserveAccepted(ctx, app.AcceptedMaintenanceRequest{Source: accepted, SourceDocument: document, ContentType: "application/octet-stream"}); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("binary content error = %v, want source invalid", err)
	}
	persisted := accepted
	persisted.SourceDocument = document
	conflict := document
	conflict.DocumentID = "docs/other.md"
	conflict.URI = "file:///srv/wiki/docs/other.md"
	if _, err := queue.ReserveAccepted(ctx, app.AcceptedMaintenanceRequest{Source: persisted, SourceDocument: conflict, ContentType: persisted.MediaType}); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("provenance conflict error = %v, want source invalid", err)
	}
	ready, err := store.ResumeReady(ctx, accepted.Scope, 10)
	if err != nil {
		t.Fatalf("inspect operations after rejection: %v", err)
	}
	if len(ready) != 0 || content.acceptCalls != 0 {
		t.Fatalf("invalid request mutated queue/raw: ready=%v accepts=%d", ready, content.acceptCalls)
	}
}

func TestReserveAcceptedReturnsReservationFailureWithoutRawReplay(t *testing.T) {
	ctx := context.Background()
	workspace, store, _, maintainer := newWorkflow(t, false, nil)
	envelope := sourceEnvelope([]byte("configured source text"))
	accepted, err := workspace.AcceptSource(ctx, envelope)
	if err != nil {
		t.Fatalf("accept raw source: %v", err)
	}
	document := knowl.SourceDocument{
		SourceID: testConfiguredSourceID, DocumentID: "docs/page.md", Revision: accepted.Version.Version,
		URI: "file:///srv/wiki/docs/page.md",
	}
	wantErr := errors.New("reservation unavailable")
	operations := &failingReservationStore{OperationStore: store, err: wantErr}
	content := &rejectingAcceptStore{Workspace: workspace}
	queue, err := app.NewIngestService(content, operations, store, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatalf("new failing accepted-source queue: %v", err)
	}
	_, err = queue.ReserveAccepted(ctx, app.AcceptedMaintenanceRequest{
		Source: accepted, SourceDocument: document, ContentType: accepted.MediaType,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("reservation error = %v, want injected failure", err)
	}
	if content.acceptCalls != 0 || operations.reserveCalls != 1 {
		t.Fatalf("failure calls: accepts=%d reserves=%d", content.acceptCalls, operations.reserveCalls)
	}
	ready, err := store.ResumeReady(ctx, accepted.Scope, 10)
	if err != nil {
		t.Fatalf("inspect operations after reservation failure: %v", err)
	}
	inspection, inspectErr := workspace.Inspect(ctx, accepted.Scope)
	if inspectErr != nil {
		t.Fatalf("inspect raw after reservation failure: %v", inspectErr)
	}
	if len(ready) != 0 || len(inspection.RawSources) != 1 {
		t.Fatalf("failure state: ready=%v raw=%d, want no operation and one raw revision", ready, len(inspection.RawSources))
	}
}

func TestExecutePassesBoundedSourceSummaryToContextSelection(t *testing.T) {
	ctx := context.Background()
	index := &recordingContextIndex{}
	workspace, store, service, maintainer := newWorkflow(t, false, index)
	_ = workspace
	_ = store
	_ = maintainer
	envelope := sourceEnvelope([]byte("preamble\n\n## Badger session decision\nbody"))
	if _, err := service.Ingest(ctx, envelope); err != nil {
		t.Fatalf("Ingest(): %v", err)
	}
	want := knowl.SourceSummary{Source: envelope.Source, Version: envelope.Version, Title: "Badger session decision"}
	if index.summary != want {
		t.Fatalf("SelectContext() summary = %#v, want %#v", index.summary, want)
	}
}

func TestExecuteKeepsReadDeadlineOutOfMaintainerPlan(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(workspace.Root(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	content := &deadlineContentStore{Workspace: workspace}
	maintainer := &deadlineMaintainer{}
	service, err := app.NewIngestService(content, store, store, maintainer, app.IngestOptions{
		ReadLimits: knowl.ReadLimits{Pages: 20, Bytes: 4 << 20, Characters: 32 << 10, Depth: 8, Deadline: time.Second},
	})
	if err != nil {
		t.Fatalf("new ingest service: %v", err)
	}
	submission, err := service.Submit(ctx, sourceEnvelope([]byte("source text")))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !content.schemaHasDeadline {
		t.Fatal("schema read did not receive the read deadline")
	}
	if _, err := service.Execute(ctx, submission); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !content.sourceHasDeadline {
		t.Fatal("source read did not receive the read deadline")
	}
	if maintainer.hasDeadline {
		t.Fatal("maintainer plan inherited the read deadline")
	}
}

const (
	testSourceRef          = "fixture:source-1@1"
	testConfiguredSourceID = "configured-wiki"
)

var (
	planPageContent       = []byte("---\nid: entities/one\ntitle: One\ntype: entity\nsource_refs:\n  - " + testSourceRef + "\n---\n# One\n\n[[entities/two]]\n")
	planSupportingContent = []byte("---\nid: entities/two\ntitle: Two\ntype: entity\nsource_refs:\n  - " + testSourceRef + "\n---\n# Two\n")
)

type countingMaintainer struct {
	mu      sync.Mutex
	plan    knowl.ModelEditPlan
	factory func(knowl.SchemaDocument) knowl.ModelEditPlan
	counter int
	schema  string
}

type deadlineContentStore struct {
	*contentfs.Workspace
	schemaHasDeadline bool
	sourceHasDeadline bool
}

type rejectingAcceptStore struct {
	*contentfs.Workspace
	acceptCalls int
}

type failingReservationStore struct {
	app.OperationStore
	err          error
	reserveCalls int
}

func (store *failingReservationStore) Reserve(context.Context, knowl.OperationKey, knowl.OperationMeta) (app.OperationReservation, error) {
	store.reserveCalls++
	return app.OperationReservation{}, store.err
}

func (store *rejectingAcceptStore) AcceptSource(context.Context, knowl.SourceEnvelope) (knowl.AcceptedSource, error) {
	store.acceptCalls++
	return knowl.AcceptedSource{}, errors.New("AcceptSource must not be called")
}

func (store *deadlineContentStore) Schema(ctx context.Context, scope knowl.ScopeRef) (knowl.SchemaDocument, error) {
	_, store.schemaHasDeadline = ctx.Deadline()
	return store.Workspace.Schema(ctx, scope)
}

func (store *deadlineContentStore) ReadSource(ctx context.Context, source knowl.AcceptedSource, limits knowl.ReadLimits) ([]byte, error) {
	_, store.sourceHasDeadline = ctx.Deadline()
	return store.Workspace.ReadSource(ctx, source, limits)
}

type deadlineMaintainer struct {
	hasDeadline bool
}

func (maintainer *deadlineMaintainer) Plan(ctx context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	_, maintainer.hasDeadline = ctx.Deadline()
	return withRootCatalog(input, knowl.ModelEditPlan{
		SchemaDigest: input.Schema.Digest,
		SourceRefs:   []string{testSourceRef},
		Edits: []knowl.FileEdit{
			{Path: testPagePath, Content: planPageContent},
			{Path: testPageTwoPath, Content: planSupportingContent},
		},
	}), nil
}

func (maintainer *countingMaintainer) Plan(_ context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	maintainer.counter++
	maintainer.schema = input.Schema.Digest
	if maintainer.factory != nil {
		return withRootCatalog(input, maintainer.factory(input.Schema)), nil
	}
	return withRootCatalog(input, maintainer.plan), nil
}

func withRootCatalog(input knowl.MaintenanceInput, plan knowl.ModelEditPlan) knowl.ModelEditPlan {
	if len(plan.Edits) == 0 {
		return plan
	}
	for _, edit := range plan.Edits {
		if edit.Path == testRootCatalogPath {
			return plan
		}
	}
	var root knowl.PageSnapshot
	for _, catalog := range input.Catalogs {
		if catalog.Path == testRootCatalogPath {
			root = catalog
			break
		}
	}
	if root.Path == "" {
		return plan
	}
	content := strings.TrimRight(root.Content, "\n") + "\n"
	for _, edit := range plan.Edits {
		if !strings.HasPrefix(edit.Path, "wiki/") || !strings.HasSuffix(edit.Path, ".md") || strings.HasSuffix(edit.Path, "/index.md") {
			continue
		}
		target := strings.TrimPrefix(edit.Path, "wiki/")
		content += "\n* [" + strings.TrimSuffix(filepath.Base(target), ".md") + "](" + target + ")\n"
	}
	plan.Edits = append(plan.Edits, knowl.FileEdit{Path: root.Path, ExpectedDigest: root.Digest, Content: []byte(content)})
	return plan
}

func (maintainer *countingMaintainer) schemaDigest() string {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	return maintainer.schema
}

func (maintainer *countingMaintainer) calls() int {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	return maintainer.counter
}

type failingIndex struct{}

type recordingContextIndex struct {
	summary knowl.SourceSummary
}

func (index *recordingContextIndex) SelectContext(_ context.Context, _ knowl.ScopeRef, summary knowl.SourceSummary, _ knowl.ReadLimits) ([]knowl.PageID, error) {
	index.summary = summary
	return nil, nil
}
func (*recordingContextIndex) Search(context.Context, knowl.ScopeRef, string, knowl.ReadLimits, []knowl.SourceID) ([]knowl.PageReference, error) {
	return nil, nil
}
func (*recordingContextIndex) Links(context.Context, knowl.ScopeRef, knowl.PageID, knowl.ReadLimits) ([]knowl.LinkReference, error) {
	return nil, nil
}
func (*recordingContextIndex) Project(context.Context, knowl.ContentCommit) error { return nil }
func (*recordingContextIndex) Rebuild(context.Context, knowl.WorkspaceSnapshot) error {
	return nil
}

func (failingIndex) SelectContext(context.Context, knowl.ScopeRef, knowl.SourceSummary, knowl.ReadLimits) ([]knowl.PageID, error) {
	return nil, nil
}
func (failingIndex) Search(context.Context, knowl.ScopeRef, string, knowl.ReadLimits, []knowl.SourceID) ([]knowl.PageReference, error) {
	return nil, nil
}
func (failingIndex) Links(context.Context, knowl.ScopeRef, knowl.PageID, knowl.ReadLimits) ([]knowl.LinkReference, error) {
	return nil, nil
}
func (failingIndex) Project(context.Context, knowl.ContentCommit) error {
	return errors.New("projection unavailable")
}
func (failingIndex) Rebuild(context.Context, knowl.WorkspaceSnapshot) error {
	return errors.New("projection unavailable")
}

type failOnceProjectionIndex struct {
	mu     sync.Mutex
	failed bool
}

func (*failOnceProjectionIndex) SelectContext(context.Context, knowl.ScopeRef, knowl.SourceSummary, knowl.ReadLimits) ([]knowl.PageID, error) {
	return nil, nil
}

func (*failOnceProjectionIndex) Search(context.Context, knowl.ScopeRef, string, knowl.ReadLimits, []knowl.SourceID) ([]knowl.PageReference, error) {
	return nil, nil
}

func (*failOnceProjectionIndex) Links(context.Context, knowl.ScopeRef, knowl.PageID, knowl.ReadLimits) ([]knowl.LinkReference, error) {
	return nil, nil
}

func (index *failOnceProjectionIndex) Project(context.Context, knowl.ContentCommit) error {
	index.mu.Lock()
	defer index.mu.Unlock()
	if !index.failed {
		index.failed = true
		return errors.New("projection unavailable")
	}
	return nil
}

func (*failOnceProjectionIndex) Rebuild(context.Context, knowl.WorkspaceSnapshot) error { return nil }

var errOutcomeUnavailable = errors.New("outcome unavailable")

type failOnceOutcomeStore struct {
	app.OperationStore
	mu     sync.Mutex
	failed bool
}

func (store *failOnceOutcomeStore) CommitOutcome(ctx context.Context, id knowl.OperationID, commit knowl.ContentCommit) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.failed {
		store.failed = true
		return errOutcomeUnavailable
	}
	return store.OperationStore.CommitOutcome(ctx, id, commit)
}

func newWorkflow(t *testing.T, autoApply bool, indexOverride app.SearchIndex, factory ...func(knowl.SchemaDocument) knowl.ModelEditPlan) (*contentfs.Workspace, *sqlite.Store, *app.IngestService, *countingMaintainer) {
	t.Helper()
	return newWorkflowWithOptions(t, app.IngestOptions{AutoApply: autoApply}, indexOverride, factory...)
}

func newWorkflowWithOptions(t *testing.T, options app.IngestOptions, indexOverride app.SearchIndex, factory ...func(knowl.SchemaDocument) knowl.ModelEditPlan) (*contentfs.Workspace, *sqlite.Store, *app.IngestService, *countingMaintainer) {
	t.Helper()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	store, err := sqlite.Open(context.Background(), filepath.Join(workspace.Root(), "state.db"))
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	schema, err := workspace.Schema(context.Background(), "local")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	maintainer := &countingMaintainer{plan: knowl.ModelEditPlan{
		SchemaDigest: schema.Digest,
		SourceRefs:   []string{testSourceRef},
		Edits: []knowl.FileEdit{
			{Path: testPagePath, Content: planPageContent},
			{Path: "wiki/entities/two.md", Content: planSupportingContent},
		},
	}}
	if len(factory) > 0 && factory[0] != nil {
		maintainer.factory = factory[0]
	}
	if indexOverride == nil {
		indexOverride = store
	}
	service, err := app.NewIngestService(workspace, store, indexOverride, maintainer, options)
	if err != nil {
		t.Fatalf("new ingest service: %v", err)
	}
	return workspace, store, service, maintainer
}

func claimReady(t *testing.T, store *sqlite.Store, scope knowl.ScopeRef) knowl.WorkClaim {
	t.Helper()
	claim, err := store.ClaimReady(context.Background(), scope, knowl.WorkLease{
		Token:     "test-worker",
		ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("claim ready operation: %v", err)
	}
	return claim
}

func sourceEnvelope(content []byte) knowl.SourceEnvelope {
	return knowl.SourceEnvelope{
		Scope:     testSourceScope,
		Source:    knowl.SourceRef{Adapter: "fixture", ID: "source-1"},
		Version:   knowl.SourceVersion{Version: "1", Digest: digest(content)},
		MediaType: "text/plain",
		Content:   content,
	}
}

func digest(content []byte) string {
	value := sha256.Sum256(content)
	return hex.EncodeToString(value[:])
}

func contains(value, want string) bool {
	for index := 0; index+len(want) <= len(value); index++ {
		if value[index:index+len(want)] == want {
			return true
		}
	}
	return false
}
