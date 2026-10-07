package hybrid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/lexical"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"golang.org/x/text/unicode/norm"
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
	parts := []string{fields.Title, fields.Tags, fields.Description, fields.Body}
	raw := strings.Join(parts, "\n\n")
	span := prepared.Windows[chunk.Ordinal]
	start, end, ok := originalWindow(raw, span)
	if !ok {
		return "", failure(knowl.RetrievalProjectionDrift)
	}
	selected := make([]string, 0, len(parts))
	tagOnly := true
	position := 0
	for index, part := range parts {
		partEnd := position + len(part)
		if left, right := max(start, position), min(end, partEnd); left < right {
			selected = append(selected, part[left-position:right-position])
			tagOnly = tagOnly && index == 1
		}
		position = partEnd + 2
	}
	original := strings.Join(selected, "\n\n")
	if tagOnly && len(selected) > 0 {
		original = "tag: " + original
	}
	return lexical.Excerpt(original, "", "", nil, characters), nil
}

func originalWindow(raw string, window TextWindow) (int, int, bool) {
	pre := strings.ReplaceAll(raw, "\r\n", "\n")
	normalized := norm.NFC.String(pre)
	leading := utf8.RuneCountInString(normalized) - utf8.RuneCountInString(strings.TrimLeftFunc(normalized, unicode.IsSpace))
	start, end := leading+window.Start, leading+window.End
	if start < 0 || end <= start || end > utf8.RuneCountInString(normalized) {
		return 0, 0, false
	}
	// Each collapsed CRLF adds one byte to positions in the original string.
	crlf := make([]int, 0)
	for i, j := 0, 0; i < len(raw); {
		if i+1 < len(raw) && raw[i] == '\r' && raw[i+1] == '\n' {
			crlf = append(crlf, j)
			i += 2
			j++
		} else {
			i++
			j++
		}
	}
	rawPosition := func(pos int) int { return pos + sort.SearchInts(crlf, pos) }
	var iterator norm.Iter
	iterator.InitString(norm.NFC, pre)
	normalizedPos, rawStart, rawEnd := 0, -1, -1
	for !iterator.Done() {
		preStart := iterator.Pos()
		segment := iterator.Next()
		preEnd := iterator.Pos()
		segmentEnd := normalizedPos + utf8.RuneCount(segment)
		if rawStart < 0 && start < segmentEnd {
			rawStart = rawPosition(preStart)
		}
		if end <= segmentEnd {
			rawEnd = rawPosition(preEnd)
			break
		}
		normalizedPos = segmentEnd
	}
	if rawStart < 0 || rawEnd < rawStart || rawEnd > len(raw) {
		return 0, 0, false
	}
	return rawStart, rawEnd, true
}
