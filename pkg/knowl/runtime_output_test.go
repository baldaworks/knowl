package knowl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

type outputConfigurationFixture struct {
	provider.Fixture
	source, hierarchy *domain.OutputCorrectionLimits
}

func (m *outputConfigurationFixture) PlanValidated(ctx context.Context, input domain.MaintenanceInput, limits domain.OutputCorrectionLimits, validate func(domain.ModelEditPlan) error) (domain.ModelEditPlan, domain.OperationCorrectionReport, error) {
	m.source = &limits
	plan, err := m.Plan(ctx, input)
	if err == nil {
		err = validate(plan)
	}
	encoded, _ := json.Marshal(plan)
	return plan, configurationReport(limits, len(encoded)), err
}

func (m *outputConfigurationFixture) PlanHierarchyValidated(_ context.Context, input domain.HierarchyInput, limits domain.OutputCorrectionLimits, validate func(domain.HierarchyModelPlan) error) (domain.HierarchyModelPlan, domain.OperationCorrectionReport, error) {
	m.hierarchy = &limits
	plan := domain.HierarchyModelPlan{SchemaDigest: input.SchemaDigest, SnapshotDigest: input.SnapshotDigest, Catalogs: []domain.HierarchyCatalogSpec{{Path: "wiki/index.md", Title: "Knowl"}}}
	encoded, _ := json.Marshal(plan)
	return plan, configurationReport(limits, len(encoded)), validate(plan)
}

func configurationReport(limits domain.OutputCorrectionLimits, used int) domain.OperationCorrectionReport {
	turns, corrections := 1, 0
	return domain.OperationCorrectionReport{Version: 1, MaxCorrections: limits.MaxCorrections, MaxOutputBytes: limits.MaxOutputBytes, DeadlineNanos: limits.DeadlineNanos, Outcome: domain.CorrectionAccepted, Turns: &turns, Corrections: &corrections, OutputBytes: &used}
}

func TestHostOutputConfigurationReachesSourceAndHierarchy(t *testing.T) {
	for _, configured := range []*int{nil, new(int), new(1)} {
		workspace, err := contentfs.New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := workspace.Init(); err != nil {
			t.Fatal(err)
		}
		config := DefaultConfig()
		config.Workspace = workspace.Root()
		config.StorePath = filepath.Join(workspace.Root(), "state.db")
		config.Output.MaxCorrections = configured
		config.IngestOptions.Output.MaxCorrections = new(1)
		m := &outputConfigurationFixture{}
		host, err := NewHost(t.Context(), config, m)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = host.Close() })
		content := []byte("configuration evidence")
		digest := sha256.Sum256(content)
		result, err := host.service.Ingest(t.Context(), domain.SourceEnvelope{Scope: "local", Source: domain.SourceRef{Adapter: "fixture", ID: "configuration"}, Version: domain.SourceVersion{Version: "1", Digest: hex.EncodeToString(digest[:])}, MediaType: "text/plain", Content: content})
		if err != nil || result.Operation.Correction == nil {
			t.Fatalf("composed source: %+v %v", result, err)
		}
		hierarchy, err := host.hierarchy.Reconcile(t.Context(), "local")
		if err != nil || hierarchy.Operation.Correction == nil {
			t.Fatalf("composed hierarchy: %+v %v", hierarchy, err)
		}
		want, err := app.NormalizeOutputSettings(config.Output)
		if err != nil || m.source == nil || m.hierarchy == nil || *m.source != want || *m.hierarchy != want || result.Operation.Correction.MaxCorrections != want.MaxCorrections || hierarchy.Operation.Correction.MaxCorrections != want.MaxCorrections {
			t.Fatalf("configuration not propagated: source=%+v hierarchy=%+v want=%+v err=%v", m.source, m.hierarchy, want, err)
		}
	}
}
