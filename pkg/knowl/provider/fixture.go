package provider

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

// Fixture is a deterministic local Maintainer used by supported contract tests.
type Fixture struct {
	Result knowl.ModelEditPlan
	Error  error
}

var _ app.Maintainer = Fixture{}

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

func fixtureCatalogPlan(input knowl.MaintenanceInput, plan knowl.ModelEditPlan) knowl.ModelEditPlan {
	if len(plan.Edits) == 0 {
		return plan
	}
	for _, edit := range plan.Edits {
		if edit.Path == "wiki/index.md" {
			return plan
		}
	}
	var root knowl.PageSnapshot
	for _, catalog := range input.Catalogs {
		if catalog.Path == "wiki/index.md" {
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
