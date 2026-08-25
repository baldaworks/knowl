package knowledgetest

import (
	"context"
	"fmt"
	"reflect"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// Projection is the narrow rebuildable search surface used by the golden
// corpus contract.
type Projection interface {
	Rebuild(ctx context.Context, snapshot knowl.WorkspaceSnapshot) error
	Search(ctx context.Context, scope knowl.ScopeRef, query string, limits knowl.ReadLimits, sources []knowl.SourceID) ([]knowl.PageReference, error)
}

// EvaluateProjectionReplay rebuilds and evaluates the exact final corpus
// twice, proving that projection replay preserves bounded evidence.
func EvaluateProjectionReplay(ctx context.Context, projection Projection, scope knowl.ScopeRef) (Metrics, error) {
	snapshot := FinalSnapshot(scope)
	if err := ValidateFinalSnapshot(snapshot); err != nil {
		return Metrics{}, fmt.Errorf("validate golden snapshot: %w", err)
	}

	var baseline map[string][]knowl.PageReference
	var metrics Metrics
	for pass := 1; pass <= 2; pass++ {
		if err := projection.Rebuild(ctx, snapshot); err != nil {
			return Metrics{}, fmt.Errorf("rebuild golden projection pass %d: %w", pass, err)
		}
		observed := make(map[string][]knowl.PageReference, QueryCount)
		retrieve := func(ctx context.Context, query string, limits knowl.ReadLimits) ([]knowl.PageReference, error) {
			references, err := projection.Search(ctx, scope, query, limits, nil)
			if err == nil {
				observed[query] = cloneReferences(references)
			}
			return references, err
		}
		current, err := Evaluate(ctx, retrieve)
		if err != nil {
			return current, fmt.Errorf("evaluate golden projection pass %d: %w", pass, err)
		}
		if !current.Passed() {
			return current, fmt.Errorf("golden projection pass %d missed threshold: %#v", pass, current)
		}
		if pass == 1 {
			baseline = observed
			metrics = current
			continue
		}
		if !reflect.DeepEqual(observed, baseline) {
			return current, fmt.Errorf("golden evidence changed after projection replay")
		}
	}
	return metrics, nil
}

func cloneReferences(references []knowl.PageReference) []knowl.PageReference {
	clone := append([]knowl.PageReference(nil), references...)
	for index := range clone {
		clone[index].SourceRefs = append([]string(nil), clone[index].SourceRefs...)
	}
	return clone
}
