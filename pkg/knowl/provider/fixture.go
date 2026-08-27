package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const fixtureRootCatalogPath = "wiki/index.md"

// Fixture is a deterministic local Maintainer used by supported contract tests.
type Fixture struct {
	Result          knowl.ModelEditPlan
	Error           error
	HierarchyResult knowl.HierarchyModelPlan
	HierarchyError  error
}

var _ app.Maintainer = Fixture{}
var _ app.HierarchyMaintainer = Fixture{}

// Plan returns the configured plan and never performs network I/O.
func (fixture Fixture) Plan(ctx context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	if err := ctx.Err(); err != nil {
		return knowl.ModelEditPlan{}, err
	}
	if fixture.Error != nil {
		return knowl.ModelEditPlan{}, fixture.Error
	}
	if fixture.Result.SchemaDigest == "" && len(fixture.Result.SourceRefs) == 0 &&
		len(fixture.Result.Edits) == 0 && fixture.Result.Rationale == "" {
		return knowl.ModelEditPlan{
			SchemaDigest: input.Schema.Digest,
			SourceRefs:   []string{app.SourceRefKey(input.Source)},
			Rationale:    "fixture no-op maintenance",
		}, nil
	}
	return fixtureCatalogPlan(input, fixture.Result), nil
}

// PlanHierarchy returns the configured semantic graph and never performs
// network I/O. A hierarchy fixture must be explicit to avoid inventing a test
// taxonomy from source identities or paths.
func (fixture Fixture) PlanHierarchy(ctx context.Context, _ knowl.HierarchyInput) (knowl.HierarchyModelPlan, error) {
	if err := ctx.Err(); err != nil {
		return knowl.HierarchyModelPlan{}, err
	}
	if fixture.HierarchyError != nil {
		return knowl.HierarchyModelPlan{}, fixture.HierarchyError
	}
	if fixture.HierarchyResult.SchemaDigest == "" && fixture.HierarchyResult.SnapshotDigest == "" && len(fixture.HierarchyResult.Catalogs) == 0 {
		return knowl.HierarchyModelPlan{}, fmt.Errorf("fixture hierarchy plan is not configured")
	}
	return cloneHierarchyModelPlan(fixture.HierarchyResult), nil
}

func cloneHierarchyModelPlan(plan knowl.HierarchyModelPlan) knowl.HierarchyModelPlan {
	cloned := plan
	cloned.Catalogs = make([]knowl.HierarchyCatalogSpec, len(plan.Catalogs))
	for index, catalog := range plan.Catalogs {
		catalog.Children = append([]string(nil), catalog.Children...)
		cloned.Catalogs[index] = catalog
	}
	return cloned
}

func fixtureCatalogPlan(input knowl.MaintenanceInput, plan knowl.ModelEditPlan) knowl.ModelEditPlan {
	if len(plan.Edits) == 0 {
		return plan
	}
	for _, edit := range plan.Edits {
		if edit.Path == fixtureRootCatalogPath {
			return plan
		}
	}
	var root knowl.PageSnapshot
	for _, catalog := range input.Catalogs {
		if catalog.Path == fixtureRootCatalogPath {
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
