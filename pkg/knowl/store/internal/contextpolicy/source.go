package contextpolicy

import (
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/lexical"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/baldaworks/knowl/pkg/knowl/wiki"
)

// SourceQuery constructs bounded semantic maintenance terms. Identity is used
// only when semantic fields yield no usable terms, never on a search miss.
func SourceQuery(source knowl.SourceSummary) lexical.Query {
	bounded := wiki.BoundSourceSignals(source)
	parts := []string{bounded.Title}
	parts = append(parts, bounded.Tags...)
	parts = append(parts, bounded.Headings...)
	parts = append(parts, bounded.Body)
	query := lexical.Summarize(parts...)
	if len(query.Terms) > 0 {
		return query
	}
	fallback := wiki.BoundSourceSignals(knowl.SourceSummary{Title: source.Source.ID, Headings: []string{source.Source.Adapter}})
	return lexical.Summarize(append([]string{fallback.Title}, fallback.Headings...)...)
}
