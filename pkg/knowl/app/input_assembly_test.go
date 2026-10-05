package app_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const budgetStorageID knowl.PageID = "decisions/storage"
const budgetSmallID knowl.PageID = "decisions/decoy-01"
const budgetSourceComponent = "source"
const budgetCatalogComponent = "catalogs"
const budgetRequiredLimitReason = "required_input_limit"

type orderedBudgetIndex struct {
	app.SearchIndex
	ids []knowl.PageID
}

func (i orderedBudgetIndex) SelectContext(context.Context, knowl.ScopeRef, knowl.SourceSummary, knowl.ReadLimits) ([]knowl.PageID, error) {
	return i.ids, nil
}

type wrappedBudgetMaintainer struct {
	baselineMaintainer
	cap   int
	calls int
}

func (m *wrappedBudgetMaintainer) RequestBudget() knowl.MaintenanceRequestBudget {
	return knowl.MaintenanceRequestBudget{MaxBytes: m.cap, FormatVersion: "fixture-wrapper-v1"}
}
func (*wrappedBudgetMaintainer) RequestBytes(ctx context.Context, input knowl.MaintenanceInput) (int, error) {
	encoded, err := app.EncodeSourceMaintenanceRequest(ctx, input)
	return len(encoded) + 100, err
}
func (m *wrappedBudgetMaintainer) Plan(ctx context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	m.calls++
	return m.baselineMaintainer.Plan(ctx, input)
}

func TestIngestFitsWholePagesInPriorityOrder(t *testing.T) {
	var previous knowl.MaintenanceBudgetReport
	for pass := range 2 {
		workspace, store, _, _ := newBaselineIngest(t)
		seedBaselineContext(t, workspace, store)
		largePath := "wiki/decisions/decoy-00.md"
		old, err := os.ReadFile(filepath.Join(workspace.Root(), largePath))
		if err != nil {
			t.Fatal(err)
		}
		writeBaselineFile(t, workspace.Root(), largePath, string(old)+strings.Repeat("large evidence ", 2000), time.Unix(0, 0))
		ids := []knowl.PageID{"index", budgetStorageID, budgetStorageID, "decisions/decoy-00", budgetSmallID, "log"}
		maintainer := &wrappedBudgetMaintainer{cap: 12000}
		service, err := app.NewIngestService(workspace, store, orderedBudgetIndex{store, ids}, maintainer, app.IngestOptions{InputLimits: knowl.MaintenanceInputLimits{MaxRequestBytes: 15000}})
		if err != nil {
			t.Fatal(err)
		}
		before, err := workspace.Snapshot(t.Context(), "local")
		if err != nil {
			t.Fatal(err)
		}
		source := "# Input\nFull source preserved: 世界 & < >."
		result, err := service.Ingest(t.Context(), sourceEnvelope([]byte(source)))
		if err != nil {
			t.Fatal(err)
		}
		pages := maintainer.input.Pages
		got := make([]knowl.PageID, 0, len(pages))
		for _, page := range pages {
			got = append(got, page.ID)
			full, readErr := os.ReadFile(filepath.Join(workspace.Root(), page.Path))
			if readErr != nil || page.Content != string(full) || page.Body == "" {
				t.Fatalf("incomplete page %s: %v", page.ID, readErr)
			}
		}
		if !reflect.DeepEqual(got, []knowl.PageID{budgetStorageID, budgetSmallID}) {
			t.Fatalf("included IDs = %v", got)
		}
		used, err := maintainer.RequestBytes(t.Context(), maintainer.input)
		if err != nil {
			t.Fatal(err)
		}
		want := knowl.MaintenanceBudgetReport{MaxBytes: 12000, UsedBytes: used, IncludedCount: 2, OmittedCount: 1}
		if result.Budget == nil || *result.Budget != want || used > 12000 || maintainer.input.SourceText != source || maintainer.input.InputLimits.MaxRequestBytes != 12000 {
			t.Fatalf("report=%+v input cap=%d want=%+v", result.Budget, maintainer.input.InputLimits.MaxRequestBytes, want)
		}
		contextReport := result.Operation.Context
		wantPages := []knowl.ContextPage{{PageID: budgetStorageID, SelectionReason: knowl.ContextUnknown, Disposition: knowl.ContextIncluded}, {PageID: "decisions/decoy-00", SelectionReason: knowl.ContextUnknown, Disposition: knowl.ContextBudgetOmitted}, {PageID: budgetSmallID, SelectionReason: knowl.ContextUnknown, Disposition: knowl.ContextIncluded}}
		if contextReport == nil || contextReport.Outcome != knowl.ContextAssembled || contextReport.CandidateCount == nil || *contextReport.CandidateCount != 3 || contextReport.CatalogCount == nil || *contextReport.CatalogCount != len(maintainer.input.Catalogs) || !reflect.DeepEqual(contextReport.Pages, wantPages) || contextReport.Budget == nil || contextReport.Budget.UsedBytes == nil || *contextReport.Budget.UsedBytes != used || contextReport.Budget.IncludedCount != 2 || contextReport.Budget.OmittedCount != 1 {
			t.Fatalf("lost actual fitting evidence: %+v", contextReport)
		}
		if pass == 1 && previous != want {
			t.Fatalf("non-deterministic budget: %+v %+v", previous, want)
		}
		previous = want
		after, err := workspace.Snapshot(t.Context(), "local")
		if err != nil || !reflect.DeepEqual(before.PageDigests, after.PageDigests) {
			t.Fatalf("planning mutated canonical: %v", err)
		}
	}
}

func TestIngestRequiredInputOverflowPreservesRaw(t *testing.T) {
	workspace, store, _, _ := newBaselineIngest(t)
	maintainer := &wrappedBudgetMaintainer{cap: 100}
	service, err := app.NewIngestService(workspace, store, store, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := workspace.Snapshot(t.Context(), "local")
	if err != nil {
		t.Fatal(err)
	}
	envelope := sourceEnvelope([]byte("Complete indispensable source"))
	result, err := service.Ingest(t.Context(), envelope)
	info, classified := app.ClassifyExecutionFailure(err)
	if !errors.Is(err, app.ErrMaintenanceInputLimit) || !classified || info.Class != "input_budget" || info.Reason != budgetRequiredLimitReason || info.Retryable || maintainer.calls != 0 || result.Operation.Status != knowl.StatusFailed {
		t.Fatalf("overflow status=%s calls=%d info=%+v err=%v", result.Operation.Status, maintainer.calls, info, err)
	}
	if report := result.Operation.Context; report == nil || report.Outcome != knowl.ContextAssemblyFailed || report.Budget == nil || report.Budget.UsedBytes != nil {
		t.Fatalf("preflight overflow invented a measurement: %+v", report)
	}
	if _, err := workspace.LoadStage(t.Context(), "local", result.Operation.ID); !errors.Is(err, app.ErrStageNotFound) {
		t.Fatalf("overflow staged: %v", err)
	}
	after, err := workspace.Snapshot(t.Context(), "local")
	if err != nil || !reflect.DeepEqual(before.PageDigests, after.PageDigests) {
		t.Fatalf("changed canonical: %v", err)
	}
	inspection, err := workspace.Inspect(t.Context(), "local")
	if err != nil || len(inspection.RawSources) != 1 || !inspection.RawSources[0].Valid {
		t.Fatalf("raw missing/invalid: %v", err)
	}
	raw, err := workspace.ReadSource(t.Context(), inspection.RawSources[0].Source, app.DefaultReadLimits())
	if err != nil || string(raw) != string(envelope.Content) {
		t.Fatalf("raw altered: %v", err)
	}
}

type interruptedBudgetContent struct {
	app.ContentStore
	cancel  context.CancelFunc
	readErr error
	reads   int
}

func (c *interruptedBudgetContent) ReadPages(ctx context.Context, scope knowl.ScopeRef, ids []knowl.PageID, limits knowl.ReadLimits) ([]knowl.PageSnapshot, error) {
	c.reads++
	if c.readErr != nil {
		return nil, c.readErr
	}
	pages, err := c.ContentStore.ReadPages(ctx, scope, ids, limits)
	if c.cancel != nil {
		c.cancel()
	}
	return pages, err
}

func TestIngestBudgetDoesNotHideReadFailureOrCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "read_failure", true: "cancellation"}[cancel], func(t *testing.T) {
			workspace, store, _, _ := newBaselineIngest(t)
			seedBaselineContext(t, workspace, store)
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			sentinel := errors.New("fixture read failed")
			content := &interruptedBudgetContent{ContentStore: workspace, readErr: sentinel}
			if cancel {
				content.readErr = nil
				content.cancel = stop
				sentinel = context.Canceled
			}
			maintainer := &wrappedBudgetMaintainer{cap: 12000}
			service, err := app.NewIngestService(content, store, orderedBudgetIndex{store, []knowl.PageID{budgetStorageID}}, maintainer, app.IngestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Ingest(ctx, sourceEnvelope([]byte(budgetSourceComponent)))
			expectedStatus := knowl.StatusFailed
			if cancel {
				expectedStatus = knowl.StatusReceived
			}
			if !errors.Is(err, sentinel) || maintainer.calls != 0 || content.reads != 1 || result.Operation.Status != expectedStatus {
				t.Fatalf("calls=%d reads=%d status=%s err=%v", maintainer.calls, content.reads, result.Operation.Status, err)
			}
			report := result.Operation.Context
			if report == nil || report.Outcome != knowl.ContextAssemblyFailed || report.Budget == nil || report.Budget.UsedBytes == nil || report.Budget.IncludedCount != 0 || report.Budget.OmittedCount != 0 || len(report.Pages) != 1 || report.Pages[0].Disposition != knowl.ContextPending {
				t.Fatalf("failed trial claimed fitting or lost prefix: %+v", report)
			}
			if _, err := workspace.LoadStage(t.Context(), "local", result.Operation.ID); !errors.Is(err, app.ErrStageNotFound) {
				t.Fatalf("stage: %v", err)
			}
		})
	}
}

func TestIngestRequiredInputExactBoundary(t *testing.T) {
	workspace, store, _, _ := newBaselineIngest(t)
	m := &wrappedBudgetMaintainer{cap: 12000}
	emptyIndex := orderedBudgetIndex{SearchIndex: store}
	service, err := app.NewIngestService(workspace, store, emptyIndex, m, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	source := sourceEnvelope([]byte("exact indispensable source"))
	if _, err := service.Ingest(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	input := m.input
	exact := 0
	for range 5 {
		exact, err = m.RequestBytes(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		if input.InputLimits.MaxRequestBytes == exact {
			break
		}
		input.InputLimits.MaxRequestBytes = exact
	}
	for _, delta := range []int{0, -1} {
		t.Run(map[int]string{0: "exact", -1: "one_over"}[delta], func(t *testing.T) {
			w, s, _, _ := newBaselineIngest(t)
			recorder := &wrappedBudgetMaintainer{cap: exact + delta}
			workflow, err := app.NewIngestService(w, s, orderedBudgetIndex{SearchIndex: s}, recorder, app.IngestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := workflow.Ingest(t.Context(), source)
			if delta == 0 {
				if err != nil || recorder.calls != 1 || result.Budget == nil || result.Budget.UsedBytes != exact || result.Budget.MaxBytes != exact {
					t.Fatalf("exact report=%+v calls=%d err=%v", result.Budget, recorder.calls, err)
				}
			} else if !errors.Is(err, app.ErrMaintenanceInputLimit) || recorder.calls != 0 {
				t.Fatalf("one-over calls=%d err=%v", recorder.calls, err)
			}
		})
	}
}

func TestIngestFactualAllowanceExcludesControlsAndDuplicates(t *testing.T) {
	workspace, store, maintainer, _ := newBaselineIngest(t)
	seedBaselineContext(t, workspace, store)
	limits := app.DefaultReadLimits()
	limits.Pages = 2
	content := &interruptedBudgetContent{ContentStore: workspace}
	ids := []knowl.PageID{"index", budgetStorageID, budgetStorageID, "log", budgetSmallID, "decisions/decoy-02"}
	service, err := app.NewIngestService(content, store, orderedBudgetIndex{store, ids}, maintainer, app.IngestOptions{ReadLimits: limits})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Ingest(t.Context(), sourceEnvelope([]byte("factual allowance")))
	if err != nil {
		t.Fatal(err)
	}
	got := make([]knowl.PageID, 0, len(maintainer.input.Pages))
	for _, page := range maintainer.input.Pages {
		got = append(got, page.ID)
	}
	if !reflect.DeepEqual(got, []knowl.PageID{budgetStorageID, budgetSmallID}) || content.reads != 2 || result.Budget.IncludedCount != 2 {
		t.Fatalf("IDs=%v reads=%d report=%+v", got, content.reads, result.Budget)
	}
}

func TestIngestCompleteEssentialComponentsCannotBeTruncated(t *testing.T) {
	for _, component := range []string{budgetSourceComponent, "schema", budgetCatalogComponent} {
		t.Run(component, func(t *testing.T) {
			workspace, store, _, _ := newBaselineIngest(t)
			text := "bounded source"
			switch component {
			case budgetSourceComponent:
				text = strings.Repeat("<", 3000) // Serialized escaping, while the raw read fits.
			case "schema":
				target := filepath.Join(workspace.Root(), "schema.md")
				original, err := os.ReadFile(target)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, append(original, []byte(strings.Repeat("schema prose ", 4000))...), 0o600); err != nil {
					t.Fatal(err)
				}
			case budgetCatalogComponent:
				target := filepath.Join(workspace.Root(), "wiki/index.md")
				root, err := os.ReadFile(target)
				if err != nil {
					t.Fatal(err)
				}
				for i := range 80 {
					name := fmt.Sprintf("required-catalog-%02d", i)
					writeBaselineFile(t, workspace.Root(), "wiki/"+name+"/index.md", "# Required catalog\n", time.Unix(0, 0))
					root = append(root, []byte("\n* [Required]("+name+"/index.md)\n")...)
				}
				if err := os.WriteFile(target, root, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			maintainer := &wrappedBudgetMaintainer{cap: 10000}
			content := &interruptedBudgetContent{ContentStore: workspace}
			service, err := app.NewIngestService(content, store, store, maintainer, app.IngestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			before, err := workspace.Snapshot(t.Context(), "local")
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Ingest(t.Context(), sourceEnvelope([]byte(text)))
			failure, classified := app.ClassifyExecutionFailure(err)
			if !errors.Is(err, app.ErrMaintenanceInputLimit) || !classified || failure.Reason != budgetRequiredLimitReason || failure.Retryable || maintainer.calls != 0 || content.reads != 0 || result.Operation.Status != knowl.StatusFailed {
				t.Fatalf("component=%s calls=%d reads=%d failure=%+v err=%v", component, maintainer.calls, content.reads, failure, err)
			}
			after, err := workspace.Snapshot(t.Context(), "local")
			if err != nil || !reflect.DeepEqual(before.PageDigests, after.PageDigests) || before.SchemaDigest != after.SchemaDigest {
				t.Fatalf("changed canonical/schema: %v", err)
			}
			if _, err := workspace.LoadStage(t.Context(), "local", result.Operation.ID); !errors.Is(err, app.ErrStageNotFound) {
				t.Fatalf("overflow staged: %v", err)
			}
			inspection, err := workspace.Inspect(t.Context(), "local")
			if err != nil || len(inspection.RawSources) != 1 || !inspection.RawSources[0].Valid {
				t.Fatalf("raw missing: %v", err)
			}
			raw, err := workspace.ReadSource(t.Context(), inspection.RawSources[0].Source, app.DefaultReadLimits())
			if err != nil || string(raw) != text {
				t.Fatalf("raw truncated: %v", err)
			}
		})
	}
}
