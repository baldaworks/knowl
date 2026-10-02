package app_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestQueuedIncompatiblePolicyNeverInvokesMaintainer(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "old contract", true: "empty legacy"}[legacy], func(t *testing.T) {
			workspace, store, service, maintainer := newWorkflow(t, false, nil)
			accepted, err := workspace.AcceptSource(t.Context(), sourceEnvelope([]byte("old queued evidence")))
			if err != nil {
				t.Fatal(err)
			}
			schema, err := workspace.Schema(t.Context(), accepted.Scope)
			if err != nil {
				t.Fatal(err)
			}
			generation := ""
			if !legacy {
				policy := app.SourceMaintenancePolicy(schema.Digest, app.DefaultReadLimits(), app.DefaultPlanLimits())
				policy.ContractVersion = "source-maintenance-v0"
				generation, err = app.MaintenancePolicyGeneration(policy)
				if err != nil {
					t.Fatal(err)
				}
			}
			key := knowl.OperationKey{Scope: accepted.Scope, Source: accepted.Source, Version: accepted.Version, MaintenanceGeneration: generation}
			reservation, err := store.Reserve(t.Context(), key, knowl.OperationMeta{Key: key, AcceptedSource: accepted, Schema: schema, SchemaDigest: schema.Digest, MaintenanceGeneration: generation})
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.RunToTerminal(t.Context(), claimReady(t, store, accepted.Scope))
			if !errors.Is(err, app.ErrMaintenancePolicyMismatch) || result.Operation.Status != knowl.StatusFailed || maintainer.calls() != 0 {
				t.Fatalf("incompatible queue ran: status=%s calls=%d err=%v", result.Operation.Status, maintainer.calls(), err)
			}
			if result.Operation.ID != reservation.ID || result.Operation.Failure == nil || result.Operation.Failure.Reason != "maintenance_policy_mismatch" {
				t.Fatalf("safe mismatch diagnostics=%v", result.Operation.Failure)
			}
			if _, err := workspace.LoadStage(t.Context(), accepted.Scope, reservation.ID); !errors.Is(err, app.ErrStageNotFound) {
				t.Fatalf("incompatible plan staged=%v", err)
			}
		})
	}
}

func TestExecuteRejectsChangedPolicyBeforeInference(t *testing.T) {
	for _, tampered := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged submission", true: "tampered generation"}[tampered], func(t *testing.T) {
			workspace, store, previous, maintainer := newWorkflow(t, false, nil)
			submission, err := previous.Submit(t.Context(), sourceEnvelope([]byte("policy change")))
			if err != nil {
				t.Fatal(err)
			}
			bounds := app.DefaultCatalogLimits()
			bounds.MaxCatalogs++
			current, err := app.NewIngestService(workspace, store, store, maintainer, app.IngestOptions{CatalogLimits: bounds})
			if err != nil {
				t.Fatal(err)
			}
			if tampered {
				schema, err := workspace.Schema(t.Context(), submission.Operation.Key.Scope)
				if err != nil {
					t.Fatal(err)
				}
				policy := app.SourceMaintenancePolicy(schema.Digest, app.DefaultReadLimits(), app.DefaultPlanLimits())
				policy.CatalogLimits = bounds
				generation, err := app.MaintenancePolicyGeneration(policy)
				if err != nil {
					t.Fatal(err)
				}
				submission.Operation.Key.MaintenanceGeneration = generation
			}
			result, err := current.Execute(t.Context(), submission)
			if !errors.Is(err, app.ErrMaintenancePolicyMismatch) || result.Operation.Status != knowl.StatusFailed || maintainer.calls() != 0 {
				t.Fatalf("policy change ran: status=%s calls=%d err=%v", result.Operation.Status, maintainer.calls(), err)
			}
		})
	}
}

func TestLegacyStageResumesWithoutInferenceAcrossPolicyChange(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		t.Run(map[bool]string{false: "synchronous", true: "scheduled"}[scheduled], func(t *testing.T) {
			workspace, store, previous, maintainer := newWorkflow(t, false, nil)
			submission, err := previous.Submit(t.Context(), sourceEnvelope([]byte("staged policy change")))
			if err != nil {
				t.Fatal(err)
			}
			schema, err := workspace.Schema(t.Context(), submission.Operation.Key.Scope)
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := workspace.Inspect(t.Context(), submission.Operation.Key.Scope)
			if err != nil {
				t.Fatal(err)
			}
			_, err = workspace.StagePlan(t.Context(), knowl.ValidatedEditPlan{OperationID: string(submission.Operation.ID), Scope: submission.Operation.Key.Scope, SchemaDigest: schema.Digest, SourceRefs: []string{testSourceRef}, Edits: []knowl.FileEdit{
				{Path: testPagePath, Content: planPageContent}, {Path: testPageTwoPath, Content: planSupportingContent},
				{Path: testRootCatalogPath, ExpectedDigest: inspection.Index.Digest, Content: []byte(inspection.Index.Content + "\n* [One](entities/one.md)\n* [Two](entities/two.md)\n")},
			}})
			if err != nil {
				t.Fatal(err)
			}
			bounds := app.DefaultCatalogLimits()
			bounds.MaxCatalogs++
			current, err := app.NewIngestService(workspace, store, store, maintainer, app.IngestOptions{CatalogLimits: bounds, AutoApply: true})
			if err != nil {
				t.Fatal(err)
			}
			var result app.IngestResult
			if scheduled {
				result, err = current.RunToTerminal(t.Context(), claimReady(t, store, submission.Operation.Key.Scope))
			} else {
				result, err = current.Execute(t.Context(), submission)
			}
			if err != nil || result.Operation.Status != knowl.StatusCommitted || maintainer.calls() != 0 {
				t.Fatalf("stage replanned: status=%s calls=%d err=%v", result.Operation.Status, maintainer.calls(), err)
			}
			replay, err := current.Execute(t.Context(), app.IngestSubmission{Operation: result.Operation})
			if err != nil || replay.Operation.Status != knowl.StatusCommitted || maintainer.calls() != 0 {
				t.Fatalf("terminal replay=%s %v", replay.Operation.Status, err)
			}
		})
	}
}

func TestCatalogPolicyGenerationChangesWithEveryEffectiveLimit(t *testing.T) {
	base := app.SourceMaintenancePolicy(strings.Repeat("a", 64), app.DefaultReadLimits(), app.DefaultPlanLimits())
	base.CatalogLimits = app.DefaultCatalogLimits()
	original, err := app.MaintenancePolicyGeneration(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*knowl.CatalogLimits){
		func(l *knowl.CatalogLimits) { l.MaxCatalogs++ }, func(l *knowl.CatalogLimits) { l.MaxEdges++ }, func(l *knowl.CatalogLimits) { l.MaxDepth++ },
		func(l *knowl.CatalogLimits) { l.MaxPathBytes-- }, func(l *knowl.CatalogLimits) { l.MaxCatalogBytes++ }, func(l *knowl.CatalogLimits) { l.MaxSnapshotBytes++ }, func(l *knowl.CatalogLimits) { l.MaxInputBytes++ },
	} {
		changed := base
		change(&changed.CatalogLimits)
		generation, err := app.MaintenancePolicyGeneration(changed)
		if err != nil || generation == original {
			t.Fatalf("catalog policy generation unchanged: %v", err)
		}
	}
}
