package knowl

import (
	"context"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

// slotLintMaintainer exposes only the base suggestion capability consumed by lint.
type slotLintMaintainer struct{ slots *executionSlots }

func (maintainer slotLintMaintainer) Plan(ctx context.Context, input domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	bounded, cancel := context.WithTimeout(ctx, app.MaxCorrectionDeadline)
	defer cancel()
	use, err := maintainer.slots.acquire(bounded)
	if err != nil {
		return domain.ModelEditPlan{}, err
	}
	defer use.release()
	if use.slot.maintainer == nil {
		return domain.ModelEditPlan{}, app.ErrMaintainerUnavailable
	}
	return use.slot.maintainer.Plan(use.ctx, input)
}
