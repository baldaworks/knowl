package contextpolicy

import (
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/lexical"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/baldaworks/knowl/pkg/knowl/wiki"
)

// SourceQuery constructs bounded semantic maintenance terms. Identity is used
// only when semantic fields yield no usable terms, never on a search miss.
func SourceQuery(source knowl.SourceSummary) (lexical.Query, error) {
	parts := []string{source.Title, source.Body}
	parts = append(parts, source.Tags[:min(len(source.Tags), 32)]...)
	parts = append(parts, source.Headings[:min(len(source.Headings), 32)]...)
	if err := lexical.ValidateQueryParts(parts...); err != nil {
		return lexical.Query{}, err
	}
	bounded := wiki.BoundSourceSignals(source)
	parts = []string{bounded.Title}
	parts = append(parts, bounded.Tags...)
	parts = append(parts, bounded.Headings...)
	parts = append(parts, bounded.Body)
	query, err := lexical.Summarize(parts...)
	if err != nil {
		return lexical.Query{}, err
	}
	if len(query.Terms) > 0 {
		return query, nil
	}
	if err := lexical.ValidateQueryParts(source.Source.ID, source.Source.Adapter); err != nil {
		return lexical.Query{}, err
	}
	fallback := wiki.BoundSourceSignals(knowl.SourceSummary{Title: source.Source.ID, Headings: []string{source.Source.Adapter}})
	return lexical.Summarize(append([]string{fallback.Title}, fallback.Headings...)...)
}
