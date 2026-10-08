package hybrid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/lexical"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// OriginalEvidence returns a bounded excerpt of the actual winning page window.
// It verifies the stored input hash and maps NFC/CRLF positions back to source bytes.
func OriginalEvidence(ctx context.Context, fields SemanticFields, space app.EmbeddingSpace, chunk Chunk, characters int) (string, error) {
	prepared, err := PreparePageFields(ctx, fields, space)
	if err != nil {
		return "", err
	}
	if chunk.Ordinal < 0 || chunk.Ordinal >= len(prepared.Inputs) {
		return "", failure(knowl.RetrievalProjectionDrift)
	}
	digest := sha256.Sum256([]byte(prepared.Inputs[chunk.Ordinal]))
	if hex.EncodeToString(digest[:]) != chunk.ContentHash {
		return "", failure(knowl.RetrievalProjectionDrift)
	}
	span := prepared.Windows[chunk.Ordinal]
	if span.Start < 0 || span.End < span.Start || span.End > len(fields.Body) {
		return "", failure(knowl.RetrievalProjectionDrift)
	}
	original := fields.Body[span.Start:span.End]
	if span.Start == span.End {
		parts := []string{fields.Title}
		if fields.OKF != nil {
			parts = append(parts, fields.OKF.Type, strings.Join(fields.OKF.Tags, "\n"), fields.OKF.Description)
		} else {
			parts = append(parts, fields.Tags, fields.Description)
		}
		selected := make([]string, 0, len(parts))
		for _, part := range parts {
			if part != "" {
				selected = append(selected, part)
			}
		}
		original = strings.Join(selected, "\n\n")
	}
	if characters <= 0 {
		characters = lexical.DefaultSnippetRunes
	}
	characters = min(characters, lexical.MaxSnippetRunes)
	runes := []rune(original)
	return string(runes[:min(len(runes), characters)]), nil
}
