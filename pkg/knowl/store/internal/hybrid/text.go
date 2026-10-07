// Package hybrid owns backend-independent bounded semantic preparation and retrieval.
package hybrid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/lexical"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/projectionmeta"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/baldaworks/knowl/pkg/knowl/wiki"
	"golang.org/x/text/unicode/norm"
)

const (
	ChunkRunes           = 384
	ChunkOverlap         = 64
	EmbeddingBatchChunks = 16
	QueryChunks          = 4
	MaxChunks            = 8192
	MaxCoverageBytes     = 1 << 20
	MaxProjectionBytes   = 64 << 20
	MaxSemanticBytes     = 4 << 20
	PreprocessingVersion = "semantic-nfc-v2-chunk384-overlap64-fullpage-query4"
)

// PreparedText contains bounded model inputs and explicit omitted coverage.
type PreparedText struct {
	Inputs        []string
	Windows       []TextWindow
	OmittedRunes  int
	OmittedChunks int
}

// TextWindow identifies one input's span in normalized, trimmed semantic text.
type TextWindow struct{ Start, End int }

// PrepareText uses original NFC Unicode, fixed progress and paragraph/space
// boundaries. Omission describes normalized semantic text, never authoritative edits.
func PrepareText(ctx context.Context, text, prefix string, maxChunks int) (PreparedText, error) {
	if ctx == nil {
		return PreparedText{}, failure(knowl.RetrievalInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return PreparedText{}, err
	}
	if !utf8.ValidString(text) || !utf8.ValidString(prefix) || maxChunks < 1 || maxChunks > MaxChunks {
		return PreparedText{}, failure(knowl.RetrievalInvalidInput)
	}
	if len(text) > MaxSemanticBytes || len(prefix) > 256 {
		return PreparedText{}, failure(knowl.RetrievalInputLimit)
	}
	text = norm.NFC.String(strings.ReplaceAll(text, "\r\n", "\n"))
	text = strings.TrimSpace(text)
	if err := ctx.Err(); err != nil {
		return PreparedText{}, err
	}
	runes := []rune(text)
	result := PreparedText{Inputs: make([]string, 0, maxChunks)}
	covered := 0
	for start := 0; start < len(runes); {
		if err := ctx.Err(); err != nil {
			return PreparedText{}, err
		}
		end := min(start+ChunkRunes, len(runes))
		if end < len(runes) {
			for i := end - 1; i >= end-ChunkOverlap; i-- {
				if unicode.IsSpace(runes[i]) {
					end = i + 1
					break
				}
			}
		}
		if len(result.Inputs) < maxChunks {
			chunk := strings.TrimSpace(string(runes[start:end]))
			if chunk != "" {
				result.Inputs = append(result.Inputs, prefix+chunk)
				result.Windows = append(result.Windows, TextWindow{Start: start, End: end})
				covered = end
			}
		} else {
			result.OmittedChunks++
		}
		if end == len(runes) {
			break
		}
		start = max(start+1, end-ChunkOverlap)
	}
	result.OmittedRunes = max(0, len(runes)-covered)
	return result, nil
}

// SpaceFingerprint excludes endpoint/credentials and includes every semantic
// model/preprocessing identity needed for a compatible disposable projection.
func SpaceFingerprint(space app.EmbeddingSpace) (string, error) {
	space, err := app.NormalizeEmbeddingSpace(space)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(struct {
		Space         app.EmbeddingSpace `json:"space"`
		Version       string             `json:"preprocessing"`
		Normalization string             `json:"normalization"`
	}{Space: space, Version: PreprocessingVersion, Normalization: "unit-float32-v1"})
	if err != nil {
		return "", failure(knowl.RetrievalInvalidConfiguration)
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

// PrepareQuery applies existing raw/literal validity bounds before semantic work.
func PrepareQuery(ctx context.Context, query string, space app.EmbeddingSpace) (PreparedText, error) {
	if _, err := lexical.Normalize(query); err != nil {
		return PreparedText{}, err
	}
	return PrepareText(ctx, query, space.QueryPrefix, QueryChunks)
}

// PrepareSource uses the existing generic source priority/identity fallback.
func PrepareSource(ctx context.Context, source knowl.SourceSummary, space app.EmbeddingSpace) (PreparedText, error) {
	raw := []string{source.Title, source.Body}
	raw = append(raw, source.Tags[:min(len(source.Tags), 32)]...)
	raw = append(raw, source.Headings[:min(len(source.Headings), 32)]...)
	if err := lexical.ValidateQueryParts(raw...); err != nil {
		return PreparedText{}, err
	}
	bounded := wiki.BoundSourceSignals(source)
	parts := []string{bounded.Title}
	parts = append(parts, bounded.Tags...)
	parts = append(parts, bounded.Headings...)
	parts = append(parts, bounded.Body)
	text := strings.Join(parts, "\n\n")
	if strings.TrimSpace(text) == "" {
		if err := lexical.ValidateQueryParts(source.Source.ID, source.Source.Adapter); err != nil {
			return PreparedText{}, err
		}
		identity := wiki.BoundSourceSignals(knowl.SourceSummary{Title: source.Source.ID, Headings: []string{source.Source.Adapter}})
		parts = append([]string{identity.Title}, identity.Headings...)
		text = strings.Join(parts, "\n\n")
	}
	return PrepareText(ctx, text, space.QueryPrefix, QueryChunks)
}

// PreparePage embeds only the original factual semantic fields, not metadata IDs.
func PreparePage(ctx context.Context, page knowl.PageSnapshot, space app.EmbeddingSpace) (PreparedText, error) {
	if !projectionmeta.SemanticPage(page) {
		return PreparedText{}, nil
	}
	values, err := projectionmeta.ValuesForPage(page)
	if err != nil {
		return PreparedText{}, err
	}
	return PreparePageFields(ctx, SemanticFields{Title: page.Title, Tags: values.Tags, Description: values.Description, Body: values.Body}, space)
}

// SemanticFields are the original page fields persisted by lexical projection.
type SemanticFields struct{ Title, Tags, Description, Body string }

// PreparePageFields uses the same complete page contract for build and evidence.
func PreparePageFields(ctx context.Context, page SemanticFields, space app.EmbeddingSpace) (PreparedText, error) {
	fields := []string{page.Title, page.Tags, page.Description, page.Body}
	total := 0
	for _, field := range fields {
		if !utf8.ValidString(field) {
			return PreparedText{}, failure(knowl.RetrievalInvalidInput)
		}
		total += len(field)
		if total > MaxSemanticBytes {
			return PreparedText{}, failure(knowl.RetrievalInputLimit)
		}
	}
	if total > MaxSemanticBytes-6 {
		return PreparedText{}, failure(knowl.RetrievalInputLimit)
	}
	prepared, err := PrepareText(ctx, strings.Join(fields, "\n\n"), space.PassagePrefix, MaxChunks)
	if err != nil {
		return PreparedText{}, err
	}
	if prepared.OmittedChunks != 0 || prepared.OmittedRunes != 0 {
		return PreparedText{}, failure(knowl.RetrievalProjectionCapacity)
	}
	return prepared, nil
}

func failure(code knowl.RetrievalFailure) error { return &app.EmbeddingError{Code: code} }
