package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/mcp"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	detailsCommittedScenario    = "committed"
	detailsAbsentLegacyScenario = "absent_legacy_facts"
	detailsOpaqueLegacyScenario = "opaque_legacy_plan"
	detailsOverflowScenario     = "required_overflow"
	detailsSourceSecret         = "SOURCE_BODY_SECRET_731"
	detailsQuerySecret          = "QUERY_TEXT_SECRET_932"
	detailsRationaleSecret      = "PLAN_RATIONALE_SECRET_514"
	detailsProviderSecret       = "UPSTREAM_BODY_SECRET_673"
)

type detailsMaintainer struct {
	calls      int
	requestCap int
	failure    error
}

func (m *detailsMaintainer) RequestBudget() domain.MaintenanceRequestBudget {
	return domain.MaintenanceRequestBudget{MaxBytes: m.requestCap, FormatVersion: "transport-fixture-v1"}
}

func (*detailsMaintainer) RequestBytes(ctx context.Context, input domain.MaintenanceInput) (int, error) {
	encoded, err := app.EncodeSourceMaintenanceRequest(ctx, input)
	return len(encoded), err
}

func (m *detailsMaintainer) Plan(ctx context.Context, input domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	m.calls++
	return (provider.Fixture{Error: m.failure, Result: domain.ModelEditPlan{
		SchemaDigest: input.Schema.Digest, SourceRefs: []string{app.SourceRefKey(input.Source)}, Rationale: detailsRationaleSecret,
		Edits: []domain.FileEdit{{Path: "wiki/entities/one.md", Content: []byte("---\nid: entities/one\ntitle: One\ntype: entity\nsource_refs:\n  - " + app.SourceRefKey(input.Source) + "\n---\n# One\n\n[[entities/missing]]\n")}},
	}}).Plan(ctx, input)
}

type detailsWireOperation struct {
	ID        string                   `json:"id"`
	Status    string                   `json:"status"`
	UpdatedAt time.Time                `json:"updated_at"`
	Failure   *domain.Failure          `json:"failure,omitempty"`
	Retrieval *domain.RetrievalStatus  `json:"retrieval,omitempty"`
	Details   *domain.OperationDetails `json:"details,omitempty"`
}

func TestOperationDetailsHTTPAndMCPDurableParity(t *testing.T) {
	for _, scenario := range []string{httpQueuedStatus, detailsCommittedScenario, "provider_failure", detailsOverflowScenario} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			workspace, err := contentfs.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := workspace.Init(); err != nil {
				t.Fatal(err)
			}
			storePath := filepath.Join(workspace.Root(), "details.db")
			store, err := sqlite.Open(ctx, storePath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			maintainer := &detailsMaintainer{requestCap: app.MaxMaintenanceRequestBytes}
			if scenario == detailsOverflowScenario {
				maintainer.requestCap = 100
			}
			if scenario == "provider_failure" {
				maintainer.failure = errors.New(detailsProviderSecret)
			}
			ingest, err := app.NewIngestService(workspace, store, store, maintainer, app.IngestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			sourceText := "# " + detailsQuerySecret + "\n\n" + detailsSourceSecret + "\n[[entities/missing]]"
			envelope, err := httpIngestEnvelope(httpTestScope, knowlapi.IngestRequest{Content: &sourceText})
			if err != nil {
				t.Fatal(err)
			}
			submission, err := ingest.Submit(ctx, envelope)
			if err != nil {
				t.Fatal(err)
			}
			var staged domain.StagedChange
			if scenario != httpQueuedStatus {
				claim, err := store.ClaimReady(ctx, httpTestScope, domain.WorkLease{Token: "details-worker", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				result, runErr := ingest.RunToTerminal(ctx, claim)
				if scenario == detailsCommittedScenario && (runErr != nil || result.Operation.Status != domain.StatusCommitted) {
					t.Fatalf("commit: %s %v", result.Operation.Status, runErr)
				}
				if scenario != detailsCommittedScenario && runErr == nil {
					t.Fatal("expected failure")
				}
				staged = result.Staged
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = sqlite.Open(ctx, storePath)
			if err != nil {
				t.Fatal(err)
			}
			ingest, err = app.NewIngestService(workspace, store, store, maintainer, app.IngestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			query, err := app.NewQueryService(workspace, store, store, nil, app.QueryOptions{})
			if err != nil {
				t.Fatal(err)
			}
			handler := NewHandler(Dependencies{Scope: httpTestScope, Ingest: ingest, Query: query})
			mcpServer, err := mcp.NewServer(query, ingest, &httpRecordingWaker{}, httpTestScope, app.DefaultReadLimits())
			if err != nil {
				t.Fatal(err)
			}
			before, err := workspace.Snapshot(ctx, httpTestScope)
			if err != nil {
				t.Fatal(err)
			}
			calls := maintainer.calls
			var previous detailsWireOperation
			for poll := range 2 {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/operations/"+string(submission.Operation.ID), nil))
				if response.Code != http.StatusOK {
					t.Fatalf("HTTP status=%d body=%s", response.Code, response.Body.String())
				}
				var generated knowlapi.OperationResult
				if err := json.Unmarshal(response.Body.Bytes(), &generated); err != nil {
					t.Fatal(err)
				}
				var httpValue detailsWireOperation
				if err := json.Unmarshal(response.Body.Bytes(), &httpValue); err != nil {
					t.Fatal(err)
				}
				value, err := mcpServer.Call(ctx, "knowl_operation", map[string]any{"id": string(submission.Operation.ID)})
				if err != nil {
					t.Fatal(err)
				}
				encodedMCP, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				var mcpValue detailsWireOperation
				if err := json.Unmarshal(encodedMCP, &mcpValue); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(httpValue, mcpValue) {
					t.Fatalf("transport mismatch: HTTP=%+v MCP=%+v", httpValue, mcpValue)
				}
				if httpValue.Details == nil || httpValue.Details.Execution == nil {
					t.Fatal("operation details missing")
				}
				if poll == 1 && !reflect.DeepEqual(previous, httpValue) {
					t.Fatal("poll reconstructed facts")
				}
				previous = httpValue
				assertDetailsSecretsAbsent(t, response.Body.Bytes())
				assertDetailsSecretsAbsent(t, encodedMCP)
			}
			details := previous.Details
			if scenario == httpQueuedStatus {
				if previous.Status != httpQueuedStatus || details.Context != nil || details.Retrieval != nil || details.Plan != nil || details.Execution.WorkAttempt != 0 {
					t.Fatalf("queued evidence invented: %+v", details)
				}
			} else {
				if details.Context == nil || details.Context.WorkAttempt != 1 || details.Execution.WorkAttempt != 1 || details.RetrievalAttempt == nil || *details.RetrievalAttempt != 1 || details.Retrieval == nil || details.Retrieval.Requested != domain.RetrievalLexical || details.Context.VectorProjection == nil || details.Context.VectorProjection.State != domain.VectorNotChecked {
					t.Fatalf("attempt evidence missing: %+v", details)
				}
				if scenario == detailsOverflowScenario {
					if previous.Failure == nil || previous.Failure.Reason != "required_input_limit" || details.Context.Outcome != domain.ContextAssemblyFailed || details.Context.Budget == nil || details.Context.Budget.UsedBytes != nil || details.Plan != nil || calls != 0 {
						t.Fatalf("overflow evidence: %+v failure=%+v", details, previous.Failure)
					}
				} else if details.Context.Outcome != domain.ContextAssembled || details.Context.Budget == nil || details.Context.Budget.UsedBytes == nil || calls != 1 {
					t.Fatalf("assembled evidence missing: %+v", details)
				}
				if scenario == detailsCommittedScenario {
					if previous.Status != string(knowlapi.OperationResultStatusCompleted) || details.Plan == nil || details.Plan.Digest != staged.Digest || details.Plan.FileCount == nil || *details.Plan.FileCount != len(staged.Files) || details.Execution.ApplyAttempt != 1 || len(details.Warnings) != 1 || details.Warnings[0].Code != domain.DiagnosticOriginalLinkUnresolved {
						t.Fatalf("staged summary mismatch: %+v stage=%+v", details, staged)
					}
				} else if details.Plan != nil || details.Execution.ApplyAttempt != 0 {
					t.Fatal("failed inference invented a plan/apply")
				}
				replay, err := ingest.Submit(ctx, envelope)
				if err != nil || replay.Operation.ID != submission.Operation.ID || replay.NeedsExecution() {
					t.Fatalf("terminal replay: %+v %v", replay, err)
				}
			}
			after, err := workspace.Snapshot(ctx, httpTestScope)
			if err != nil || !reflect.DeepEqual(before.PageDigests, after.PageDigests) || maintainer.calls != calls {
				t.Fatal("poll/replay mutated canonical or invoked inference")
			}
		})
	}
}

func assertDetailsSecretsAbsent(t *testing.T, payload []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatal(err)
	}
	var visit func(any)
	visit = func(value any) {
		switch v := value.(type) {
		case string:
			for _, secret := range []string{detailsSourceSecret, detailsQuerySecret, detailsRationaleSecret, detailsProviderSecret} {
				if strings.Contains(v, secret) {
					t.Fatal("public operation leaked private text")
				}
			}
		case []any:
			for _, item := range v {
				visit(item)
			}
		case map[string]any:
			for key, item := range v {
				visit(key)
				visit(item)
			}
		}
	}
	visit(value)
}

type detailsSnapshotOperations struct {
	app.OperationStore
	operation domain.Operation
}

func (s detailsSnapshotOperations) Operation(_ context.Context, scope domain.ScopeRef, id domain.OperationID) (domain.Operation, error) {
	if scope != s.operation.Key.Scope || id != s.operation.ID {
		return domain.Operation{}, app.ErrOperationNotFound
	}
	return s.operation, nil
}

type detailsReadOnlyIndex struct{ app.SearchIndex }

func (detailsReadOnlyIndex) Search(context.Context, domain.ScopeRef, string, domain.ReadLimits, []domain.SourceID) ([]domain.PageReference, error) {
	panic("operation read reran search")
}

func (detailsReadOnlyIndex) SelectContext(context.Context, domain.ScopeRef, domain.SourceSummary, domain.ReadLimits) ([]domain.PageID, error) {
	panic("operation read reran selection")
}

func TestOperationDetailsPortsPreserveBoundsAndHistoricalUnknowns(t *testing.T) {
	ctx := t.Context()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(workspace.Root(), "details.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	count := 100
	catalogs := 250
	used := app.MaxMaintenanceRequestBytes
	report := domain.OperationContextReport{Version: 1, WorkAttempt: 1, Outcome: domain.ContextAssembled, CandidateCount: &count, CatalogCount: &catalogs, Budget: &domain.ContextBudget{MaxBytes: used, UsedBytes: &used, IncludedCount: count}, VectorProjection: &domain.VectorProjectionStatus{State: domain.VectorReady}}
	for i := range count {
		report.Pages = append(report.Pages, domain.ContextPage{PageID: domain.PageID(fmt.Sprintf("pages/%03d", i) + strings.Repeat("<", 2000)), SelectionReason: domain.ContextHybrid, Disposition: domain.ContextIncluded})
	}
	report, err = app.BoundOperationContextReport(report)
	if err != nil {
		t.Fatal(err)
	}
	maxCount := 2147483647
	operation := domain.Operation{ID: "details-max", Key: domain.OperationKey{Scope: httpTestScope, Source: domain.SourceRef{ID: detailsSourceSecret}, Version: domain.SourceVersion{Version: detailsQuerySecret}}, Status: domain.StatusCommitted, UpdatedAt: time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), Context: &report, WorkAttempt: maxCount, RetryAttempt: maxCount, ManualRetryCount: maxCount, Attempt: maxCount, ReadyAt: time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), RetrievalAttempt: 1,
		Plan:      &domain.OperationPlanSummary{Digest: strings.Repeat("f", 64), FileCount: &maxCount},
		Retrieval: &domain.RetrievalReport{Requested: domain.RetrievalHybrid, Effective: domain.RetrievalHybrid, ModelSpace: strings.Repeat("f", 16), LexicalCandidates: 100, VectorCandidates: 100, FusedCandidates: 200, ScannedChunks: 8192, QueryOmittedRunes: maxCount, IndexOmittedRunes: maxCount, IndexOmittedChunks: maxCount},
	}
	for i := range 7 {
		operation.Diagnostics = append(operation.Diagnostics, domain.MaintenanceDiagnostic{Code: domain.DiagnosticOriginalLinkUnresolved, Path: fmt.Sprintf("wiki/%03d", i) + strings.Repeat("x", 2000), Target: "pages/" + strings.Repeat("x", 2000)})
	}
	operation.Diagnostics = append(operation.Diagnostics, domain.MaintenanceDiagnostic{Code: detailsProviderSecret, Path: "private"})
	for _, scenario := range []string{"maximum", detailsOpaqueLegacyScenario, detailsAbsentLegacyScenario} {
		t.Run(scenario, func(t *testing.T) {
			current := operation
			if scenario == detailsOpaqueLegacyScenario {
				current.Plan = &domain.OperationPlanSummary{Digest: detailsRationaleSecret}
			}
			if scenario == detailsAbsentLegacyScenario {
				current.Context = nil
				current.Plan = nil
				current.Retrieval = nil
				current.Diagnostics = nil
			}
			ops := detailsSnapshotOperations{OperationStore: store, operation: current}
			index := detailsReadOnlyIndex{store}
			maintainer := &detailsMaintainer{requestCap: app.MaxMaintenanceRequestBytes}
			ingest, err := app.NewIngestService(workspace, ops, index, maintainer, app.IngestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			query, err := app.NewQueryService(workspace, ops, index, nil, app.QueryOptions{})
			if err != nil {
				t.Fatal(err)
			}
			handler := NewHandler(Dependencies{Scope: httpTestScope, Ingest: ingest, Query: query})
			mcpServer, err := mcp.NewServer(query, ingest, &httpRecordingWaker{}, httpTestScope, app.DefaultReadLimits())
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/operations/"+string(current.ID), nil))
			if response.Code != http.StatusOK {
				t.Fatalf("HTTP %d %s", response.Code, response.Body.String())
			}
			var httpValue detailsWireOperation
			if err := json.Unmarshal(response.Body.Bytes(), &httpValue); err != nil {
				t.Fatal(err)
			}
			value, err := mcpServer.Call(ctx, "knowl_operation", map[string]any{"id": string(current.ID)})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var mcpValue detailsWireOperation
			if err := json.Unmarshal(encoded, &mcpValue); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(httpValue, mcpValue) || httpValue.Details == nil {
				t.Fatal("generated transport lost bounded facts")
			}
			assertDetailsSecretsAbsent(t, response.Body.Bytes())
			assertDetailsSecretsAbsent(t, encoded)
			details := httpValue.Details
			if details.Execution == nil || details.Execution.WorkAttempt != maxCount || maintainer.calls != 0 {
				t.Fatal("read reconstructed execution")
			}
			if scenario == detailsAbsentLegacyScenario {
				if details.Context != nil || details.Retrieval != nil || details.RetrievalAttempt != nil || details.Plan != nil || len(details.Warnings) != 0 {
					t.Fatal("invented legacy evidence")
				}
				return
			}
			if details.Context == nil || details.Context.WorkAttempt != 1 || details.Context.EntriesOmitted == 0 || details.RetrievalAttempt == nil || *details.RetrievalAttempt != 1 || details.Retrieval.ModelSpace != strings.Repeat("f", 16) || details.Retrieval.ScannedChunks != 8192 || len(details.Warnings) != 7 || details.WarningsOmitted != 1 {
				t.Fatal("historical attribution/truncation lost")
			}
			if scenario == detailsOpaqueLegacyScenario && details.Plan != nil {
				t.Fatal("opaque legacy plan leaked")
			}
			if scenario == "maximum" {
				if details.Plan == nil || details.Plan.FileCount == nil || *details.Plan.FileCount != maxCount {
					t.Fatal("known file count lost")
				}
				payload, err := json.Marshal(details)
				if err != nil || len(payload) < 48<<10 || len(payload) >= 96<<10 {
					t.Fatalf("details bytes=%d err=%v", len(payload), err)
				}
			}
		})
	}
}

func TestOperationDetailsCorruptRequiredEvidenceFailsClosed(t *testing.T) {
	ctx := t.Context()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(workspace.Root(), "details.db")
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ingest, err := app.NewIngestService(workspace, store, store, provider.Fixture{}, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sourceText := detailsSourceSecret
	envelope, err := httpIngestEnvelope(httpTestScope, knowlapi.IngestRequest{Content: &sourceText})
	if err != nil {
		t.Fatal(err)
	}
	submission, err := ingest.Submit(ctx, envelope)
	if err != nil {
		t.Fatal(err)
	}
	query, err := app.NewQueryService(workspace, store, detailsReadOnlyIndex{store}, nil, app.QueryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Dependencies{Scope: httpTestScope, Ingest: ingest, Query: query})
	mcpServer, err := mcp.NewServer(query, ingest, &httpRecordingWaker{}, httpTestScope, app.DefaultReadLimits())
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	zero := 0
	report := domain.OperationContextReport{Version: 1, Outcome: domain.ContextAssemblyFailed, CandidateCount: &zero, Budget: &domain.ContextBudget{MaxBytes: 4096}}
	valid, err := app.EncodeOperationContextReport(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"work_attempt", "entries_omitted", "budget.included_count", "budget.omitted_count"} {
		for _, missing := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/missing=%t", field, missing), func(t *testing.T) {
				var root map[string]any
				if err := json.Unmarshal([]byte(valid), &root); err != nil {
					t.Fatal(err)
				}
				object := root
				parent, name, nested := strings.Cut(field, ".")
				if nested {
					object = root[parent].(map[string]any)
				} else {
					name = parent
				}
				if missing {
					delete(object, name)
				} else {
					object[name] = nil
				}
				payload, err := json.Marshal(root)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, `UPDATE knowl_operations SET context_report=? WHERE operation_id=?`, string(payload), submission.Operation.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := store.Operation(ctx, httpTestScope, submission.Operation.ID); !errors.Is(err, app.ErrOperationContextReportInvalid) {
					t.Fatalf("stored corruption accepted: %v", err)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/operations/"+string(submission.Operation.ID), nil))
				var failure knowlapi.ErrorResponse
				if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
					t.Fatal(err)
				}
				if response.Code != http.StatusUnprocessableEntity || failure.Error != "operation_failed" {
					t.Fatalf("unsafe HTTP error: %d %+v", response.Code, failure)
				}
				assertDetailsSecretsAbsent(t, response.Body.Bytes())
				if _, err := mcpServer.Call(ctx, "knowl_operation", map[string]any{"id": string(submission.Operation.ID)}); !errors.Is(err, app.ErrOperationContextReportInvalid) {
					t.Fatalf("MCP corruption accepted: %v", err)
				}
			})
		}
	}
}
