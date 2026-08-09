package provider

import (
	"context"

	"github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
)

// Fixture is a deterministic local Maintainer used by supported contract tests.
type Fixture struct {
	Result knowl.ModelEditPlan
	Error  error
}

var _ app.Maintainer = Fixture{}

// Plan returns the configured plan and never performs network I/O.
func (fixture Fixture) Plan(ctx context.Context, _ knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	if err := ctx.Err(); err != nil {
		return knowl.ModelEditPlan{}, err
	}
	if fixture.Error != nil {
		return knowl.ModelEditPlan{}, fixture.Error
	}
	return fixture.Result, nil
}
