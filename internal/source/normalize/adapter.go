package normalize

import (
	"context"
	"fmt"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// Adapter implements app.SourceNormalizer over the catalog and render core.
type Adapter struct {
	limits Limits
}

// NewAdapter returns a bounded application normalizer adapter.
func NewAdapter(limits Limits) *Adapter {
	return &Adapter{limits: limits}
}

// NewDefaultAdapter returns an adapter with conservative default bounds.
func NewDefaultAdapter() *Adapter {
	return &Adapter{limits: DefaultLimits()}
}

// NormalizeSource renders one raw document deterministically into detached
// canonical write mutations and independent format, catalog, and mirror
// identities. Context is validated before and after the bounded work.
func (adapter *Adapter) NormalizeSource(ctx context.Context, input app.SourceNormalizationInput) (app.SourceNormalizationResult, error) {
	if err := ctx.Err(); err != nil {
		return app.SourceNormalizationResult{}, err
	}
	catalog, err := BuildCatalog(input.Catalog, adapter.limits)
	if err != nil {
		return app.SourceNormalizationResult{}, fmt.Errorf("build source catalog: %w", err)
	}
	result, err := Render(RenderInput{
		Source: input.Source, Document: input.Document, RawSource: input.RawSource, Catalog: catalog,
	}, adapter.limits)
	if err != nil {
		return app.SourceNormalizationResult{}, fmt.Errorf("render source document: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return app.SourceNormalizationResult{}, err
	}
	files := result.Files()
	mutations := make([]knowl.SourceMutation, 0, len(files))
	for _, file := range files {
		mutations = append(mutations, knowl.SourceMutation{
			Action: knowl.SourceMutationWrite, Path: file.Path(), Content: file.Content(),
		})
	}
	validated, err := app.NormalizeSourceMutationPlan(knowl.SourceMutationPlan{
		RunID: "normalizer-output", Scope: input.RawSource.Scope, SourceID: input.Source.ID, Mutations: mutations,
	})
	if err != nil {
		return app.SourceNormalizationResult{}, fmt.Errorf("validate normalized mutations: %w", err)
	}
	return app.SourceNormalizationResult{
		FormatVersion: result.FormatVersion(),
		CatalogDigest: result.CatalogDigest(),
		MirrorDigest:  result.MirrorDigest(),
		Mutations:     validated.Mutations,
		Diagnostics:   result.Diagnostics(),
	}, nil
}
