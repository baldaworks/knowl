package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const historicContractV1 = "source-maintenance-v1"
const historicContractV2 = "source-maintenance-v2"
const historicContractV3 = "source-maintenance-v3"
const historicContractV4 = "source-maintenance-v4"
const historicContractV5 = "source-maintenance-v5"

func TestQueuedIncompatiblePolicyNeverInvokesMaintainer(t *testing.T) {
	for _, version := range []string{historicContractV1, historicContractV2, historicContractV3, historicContractV4, historicContractV5, ""} {
		t.Run("old contract "+version, func(t *testing.T) {
			workspace, store, _, maintainer := newWorkflow(t, false, nil)
			index := &historicalInferenceIndex{SearchIndex: store}
			service, err := app.NewIngestService(workspace, store, index, maintainer, app.IngestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := workspace.AcceptSource(t.Context(), sourceEnvelope([]byte("old queued evidence")))
			if err != nil {
				t.Fatal(err)
			}
			schema, err := workspace.Schema(t.Context(), accepted.Scope)
			if err != nil {
				t.Fatal(err)
			}
			generation := ""
			if version != "" {
				generation = historicalPolicyGeneration(t, version, schema.Digest)
			}
			key := knowl.OperationKey{Scope: accepted.Scope, Source: accepted.Source, Version: accepted.Version, MaintenanceGeneration: generation}
			reservation, err := store.Reserve(t.Context(), key, knowl.OperationMeta{Key: key, AcceptedSource: accepted, Schema: schema, SchemaDigest: schema.Digest, MaintenanceGeneration: generation})
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.RunToTerminal(t.Context(), claimReady(t, store, accepted.Scope))
			if !errors.Is(err, app.ErrMaintenancePolicyMismatch) || result.Operation.Status != knowl.StatusFailed || maintainer.calls() != 0 || index.inferenceCalls != 0 {
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
	for _, version := range []string{historicContractV1, historicContractV2, historicContractV3, historicContractV4, historicContractV5} {
		for _, scheduled := range []bool{false, true} {
			t.Run(version+"/"+map[bool]string{false: "synchronous", true: "scheduled"}[scheduled], func(t *testing.T) {
				workspace, store, _, maintainer := newWorkflow(t, false, nil)
				accepted, err := workspace.AcceptSource(t.Context(), sourceEnvelope([]byte("staged policy change")))
				if err != nil {
					t.Fatal(err)
				}
				schema, err := workspace.Schema(t.Context(), accepted.Scope)
				if err != nil {
					t.Fatal(err)
				}
				generation := historicalPolicyGeneration(t, version, schema.Digest)
				key := knowl.OperationKey{Scope: accepted.Scope, Source: accepted.Source, Version: accepted.Version, MaintenanceGeneration: generation}
				reservation, err := store.Reserve(t.Context(), key, knowl.OperationMeta{Key: key, AcceptedSource: accepted, Schema: schema, SchemaDigest: schema.Digest, MaintenanceGeneration: generation})
				if err != nil {
					t.Fatal(err)
				}
				submission := app.IngestSubmission{Operation: reservation.Operation}
				schema, err = workspace.Schema(t.Context(), submission.Operation.Key.Scope)
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
				index := &historicalInferenceIndex{SearchIndex: store}
				current, err := app.NewIngestService(workspace, store, index, maintainer, app.IngestOptions{CatalogLimits: bounds, InputLimits: knowl.MaintenanceInputLimits{MaxRequestBytes: 100}, AutoApply: true})
				if err != nil {
					t.Fatal(err)
				}
				var result app.IngestResult
				if scheduled {
					result, err = current.RunToTerminal(t.Context(), claimReady(t, store, submission.Operation.Key.Scope))
				} else {
					result, err = current.Execute(t.Context(), submission)
				}
				if err != nil || result.Operation.Status != knowl.StatusCommitted || maintainer.calls() != 0 || index.inferenceCalls != 0 {
					t.Fatalf("stage replanned: status=%s calls=%d err=%v", result.Operation.Status, maintainer.calls(), err)
				}
				replay, err := current.Execute(t.Context(), app.IngestSubmission{Operation: result.Operation})
				if err != nil || replay.Operation.Status != knowl.StatusCommitted || maintainer.calls() != 0 || index.inferenceCalls != 0 {
					t.Fatalf("terminal replay=%s %v", replay.Operation.Status, err)
				}
			})
		}
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

// Frozen payloads were emitted by the normalized implementations at 9e9edf0
// (v1), fddb1a3 (v2), 0a1d133 (v3), and c929d69 (v4). Never retrofit them through current policy.
func historicalPolicyGeneration(t *testing.T, version, schemaDigest string) string {
	t.Helper()
	filename := map[string]string{historicContractV1: "v1.json", historicContractV2: "v2.json", historicContractV3: "v3.json", historicContractV4: "v4.json", historicContractV5: "v5.json"}[version]
	encoded, err := os.ReadFile(filepath.Join("testdata", "maintenance-policy", filename))
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		CatalogLimits        json.RawMessage `json:"catalog_limits,omitempty"`
		InputLimits          json.RawMessage `json:"input_limits,omitempty"`
		RequestFormatVersion string          `json:"request_format_version,omitempty"`
		ContractVersion      string          `json:"contract_version"`
		SchemaDigest         string          `json:"schema_digest"`
		ReadLimits           json.RawMessage `json:"read_limits"`
		PlanLimits           json.RawMessage `json:"plan_limits"`
	}
	if err := json.Unmarshal(encoded, &payload); err != nil || payload.ContractVersion != version {
		t.Fatalf("historical payload: %v", err)
	}
	payload.SchemaDigest = schemaDigest
	encoded, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func TestHistoricalMaintenanceGenerationsRemainAuthentic(t *testing.T) {
	hashes := map[string]string{
		historicContractV1: "ca4b71538979962f984b558495fe2e6fcf32e877be0d80ce2b52f7e9524f2913",
		historicContractV2: "6153684971b79cb7a22632607c6f179ae28f2e3bdd1dd58117cf85740cafdc40",
		historicContractV3: "e733dc8514e0ef90a6e41ac33b2ea4aaf311b8832fc71ebf98bd72c1a1e06c83",
		historicContractV4: "73fa3a07f730f3d6dceef78e863c67786bce93e1b682b4b9a7f3eac9a74c7453",
	}
	for version, want := range hashes {
		got := historicalPolicyGeneration(t, version, strings.Repeat("a", 64))
		if got != want {
			t.Fatalf("historical %s generation=%s want=%s", version, got, want)
		}
		policy := app.SourceMaintenancePolicy(strings.Repeat("a", 64), app.DefaultReadLimits(), app.DefaultPlanLimits())
		current, err := app.MaintenancePolicyGeneration(policy)
		if err != nil || current == got {
			t.Fatalf("current generation reused historical %s: %v", version, err)
		}
		// v4 has the current payload shape; older payloads predate input sizing.
		if version == historicContractV4 {
			continue
		}
		retrofitted := app.SourceMaintenancePolicy(strings.Repeat("a", 64), app.DefaultReadLimits(), app.DefaultPlanLimits())
		retrofitted.ContractVersion = version
		current, err = app.MaintenancePolicyGeneration(retrofitted)
		if err != nil || current == got {
			t.Fatalf("historical payload was retrofitted to current shape: %v", err)
		}
	}
}

// The v5 payload was captured with the unchanged normalized implementation at
// f6f1f6040db1b6ff6b394daebf6f8422bd3c6fac before any S10 policy edits.
func TestGenuineV5MaintenanceGeneration(t *testing.T) {
	got := historicalPolicyGeneration(t, historicContractV5, strings.Repeat("a", 64))
	const want = "943b48fb34019ab19621b36c5228d73bc4de3f0bdfd65178cca4f31b54f2ff6e"
	if got != want {
		t.Fatalf("genuine v5 generation=%s want=%s", got, want)
	}
}

type historicalInferenceIndex struct {
	app.SearchIndex
	inferenceCalls int
}

func (index *historicalInferenceIndex) SelectContext(ctx context.Context, scope knowl.ScopeRef, source knowl.SourceSummary, limits knowl.ReadLimits) ([]knowl.PageID, error) {
	index.inferenceCalls++
	return index.SearchIndex.SelectContext(ctx, scope, source, limits)
}
func (index *historicalInferenceIndex) Project(ctx context.Context, commit knowl.ContentCommit) error {
	index.inferenceCalls++
	return index.SearchIndex.Project(ctx, commit)
}
func (index *historicalInferenceIndex) ProjectWithoutInference(ctx context.Context, commit knowl.ContentCommit) error {
	return index.SearchIndex.Project(ctx, commit)
}
