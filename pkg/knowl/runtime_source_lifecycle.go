package knowl

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

func reconcileConfiguredSourceLifecycles(
	ctx context.Context,
	store app.SourceLifecycleStore,
	scope domain.ScopeRef,
	sources []domain.Source,
	updatedAt time.Time,
) error {
	if store == nil {
		return app.ErrSourceLifecycleInvalid
	}
	desired := make([]app.DesiredSourceOwner, 0, len(sources))
	for _, source := range sources {
		owner, err := app.SourceOwnerFromType(source.Type, source.ID)
		if err != nil {
			return fmt.Errorf("resolve configured source owner: %w", err)
		}
		digest := source.ConfigDigest
		if digest == "" {
			digest, err = app.SourceConfigDigest(source)
			if err != nil {
				return fmt.Errorf("digest configured source: %w", err)
			}
		}
		desired = append(desired, app.DesiredSourceOwner{Owner: owner, ConfigDigest: digest})
	}
	sort.Slice(desired, func(left, right int) bool {
		if desired[left].Owner.Adapter != desired[right].Owner.Adapter {
			return desired[left].Owner.Adapter < desired[right].Owner.Adapter
		}
		return desired[left].Owner.SourceID < desired[right].Owner.SourceID
	})
	authority, err := store.SourceLifecycleAuthority(ctx, scope)
	if err != nil {
		return fmt.Errorf("read configured source lifecycle authority: %w", err)
	}
	request := app.SourceLifecycleReconcileRequest{
		Scope:                  scope,
		Desired:                desired,
		AuthorityGeneration:    app.SourceLifecycleGeneration(desired),
		ExpectedAuthorityEpoch: authority.Epoch,
		UpdatedAt:              updatedAt.UTC(),
	}
	if err := store.ReconcileSourceLifecycles(ctx, request); err != nil {
		return fmt.Errorf("reconcile configured source lifecycles: %w", err)
	}
	return nil
}
