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
	section := evidenceSection(fields.Body, span)
	if section != nil {
		parts := make([]string, 0, len(section.ancestry)+2)
		if fields.Title != "" {
			parts = append(parts, fields.Title)
		}
		for _, heading := range section.ancestry {
			parts = append(parts, fields.Body[heading.start:heading.end])
		}
		if original != "" && (section.heading.start != span.Start || section.heading.end != span.End) {
			parts = append(parts, original)
		}
		original = strings.Join(parts, "\n\n")
	} else if span.Start == span.End {
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

func evidenceSection(body string, span TextWindow) *sourceSection {
	for _, section := range parseSections(body) {
		if section.heading.end > section.heading.start && section.heading.start == span.Start && section.heading.end == span.End {
			return &section
		}
		for _, block := range section.blocks {
			if block.start <= span.Start && span.End <= block.end {
				return &section
			}
		}
		// One input can contain consecutive blocks from this section.
		if len(section.blocks) > 0 && section.blocks[0].start <= span.Start && span.End <= section.blocks[len(section.blocks)-1].end {
			return &section
		}
	}
	return nil
}
