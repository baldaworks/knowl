package knowl

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestOperationalTimeoutDefaultsAndOverrides(t *testing.T) {
	for _, test := range []struct {
		name         string
		config, want TimeoutsConfig
	}{
		{"defaults", TimeoutsConfig{}, TimeoutsConfig{15 * time.Minute, 20 * time.Minute}},
		{"larger build", TimeoutsConfig{ProjectionRebuild: time.Hour}, TimeoutsConfig{time.Hour, 65 * time.Minute}},
		{"explicit hierarchy", TimeoutsConfig{Hierarchy: time.Minute}, TimeoutsConfig{15 * time.Minute, time.Minute}},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultConfig()
			config.Workspace = t.TempDir()
			config.Timeouts = test.config
			got, err := config.normalized()
			if err != nil || got.Timeouts != test.want {
				t.Fatalf("normalized timeout=%+v err=%v", got.Timeouts, err)
			}
		})
	}
	for _, config := range []TimeoutsConfig{{ProjectionRebuild: -1}, {Hierarchy: -1}, {ProjectionRebuild: time.Duration(math.MaxInt64)}} {
		if _, err := config.Normalize(); !errors.Is(err, ErrTimeoutConfigInvalid) {
			t.Fatalf("invalid timeout=%+v err=%v", config, err)
		}
	}
}

type hierarchyDeadlineFixture struct {
	outputConfigurationFixture
	deadline       time.Time
	planningBudget time.Duration
}

type hierarchyDeadlineIndex struct {
	app.SearchIndex
	deadline time.Time
}

func (index *hierarchyDeadlineIndex) Project(ctx context.Context, commit domain.ContentCommit) error {
	index.deadline, _ = ctx.Deadline()
	return index.SearchIndex.Project(ctx, commit)
}

func (fixture *hierarchyDeadlineFixture) PlanHierarchyValidated(ctx context.Context, input domain.HierarchyInput, limits domain.OutputCorrectionLimits, validate func(domain.HierarchyModelPlan) error) (domain.HierarchyModelPlan, domain.OperationCorrectionReport, error) {
	fixture.deadline, _ = ctx.Deadline()
	fixture.planningBudget = time.Duration(limits.DeadlineNanos)
	return fixture.outputConfigurationFixture.PlanHierarchyValidated(ctx, input, limits, validate)
}

func TestHierarchyHasOperationalBudgetSeparateFromPlanning(t *testing.T) {
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig()
	config.Workspace = workspace.Root()
	config.Timeouts.ProjectionRebuild = time.Hour
	fixture := &hierarchyDeadlineFixture{}
	host, err := NewHost(t.Context(), config, fixture)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	// Reuse the Host's already-bound workspace and authority coordinator.
	content := &hostMaintenanceContent{Workspace: host.workspace, lifecycles: host.sourceLifecycle, coordinator: host.config.IngestOptions.ApplyCoordinator}
	index := &hierarchyDeadlineIndex{SearchIndex: host.index}
	service, err := app.NewHierarchyService(content, host.operations, index, fixture, hostHierarchyOptions(host.config))
	if err != nil {
		t.Fatal(err)
	}
	host.hierarchy = service
	host.slots.all[0].hierarchy = service
	// An independent context lets this test observe the configured deadline,
	// rather than the enclosing test runner's shorter deadline.
	result, err := host.ReconcileHierarchy(context.Background())
	if err != nil || result.Operation.Status != domain.StatusCommitted {
		t.Fatalf("reconcile: %+v %v", result, err)
	}
	remaining := time.Until(index.deadline)
	planningRemaining := time.Until(fixture.deadline)
	if remaining < 64*time.Minute || remaining > 65*time.Minute || fixture.planningBudget != app.MaxCorrectionDeadline || planningRemaining <= 0 || planningRemaining > app.MaxCorrectionDeadline {
		t.Fatalf("outer remaining=%v planning budget=%v", remaining, fixture.planningBudget)
	}
}
