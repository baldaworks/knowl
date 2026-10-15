package fs

import (
	"context"
	"errors"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

var _ app.RetirementProjectionReader = (*Workspace)(nil)

// RetirementProjectionPending reports whether canonical retirement content
// committed while its durable operation still awaits projection/outcome replay.
func (workspace *Workspace) RetirementProjectionPending(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (bool, error) {
	if strings.TrimSpace(string(scope)) == "" || strings.TrimSpace(string(id)) == "" {
		return false, app.ErrExecutionDescriptorUnavailable
	}
	staged, err := workspace.LoadStage(ctx, scope, id)
	if errors.Is(err, app.ErrStageNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, committed, err := workspace.RetirementCommitReceipt(ctx, staged)
	return committed, err
}
