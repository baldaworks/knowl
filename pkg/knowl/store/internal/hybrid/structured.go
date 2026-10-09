package hybrid

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/projectionmeta"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
	"golang.org/x/text/unicode/norm"
)

// Page windows are byte spans in SemanticFields.Body. Empty spans cite semantic
// metadata. Query windows remain rune spans in normalized query text.
type sourceBlock struct{ start, end int }
type sourceSection struct {
	path     string
	heading  sourceBlock
	ancestry []sourceBlock
	blocks   []sourceBlock
}

func prepareStructuredPage(ctx context.Context, page SemanticFields, space app.EmbeddingSpace) (PreparedText, error) {
	if ctx == nil {
		return PreparedText{}, failure(knowl.RetrievalInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return PreparedText{}, err
	}
	if page.Format != "" && page.Format != projectionmeta.OKFFormat {
		return PreparedText{}, failure(knowl.RetrievalInvalidInput)
	}
	if (page.Format == "") != (page.OKF == nil) {
		return PreparedText{}, failure(knowl.RetrievalInvalidInput)
	}
	fields := []string{page.Title, page.Body}
	if page.OKF != nil {
		fields = append(fields, page.OKF.Type, page.OKF.Description)
		fields = append(fields, page.OKF.Tags...)
	} else {
		fields = append(fields, page.Tags, page.Description)
	}
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
	if !utf8.ValidString(space.PassagePrefix) {
		return PreparedText{}, failure(knowl.RetrievalInvalidInput)
	}
	contextParts := []string{page.Title}
	if page.OKF != nil {
		contextParts = append(contextParts, page.OKF.Type, strings.Join(page.OKF.Tags, "\n"), page.OKF.Description)
	} else {
		contextParts = append(contextParts, page.Tags, page.Description)
	}
	contextText := normalizeSection(strings.Join(contextParts, "\n"))
	sections := parseSections(page.Body)
	result := PreparedText{}
	for _, section := range sections {
		if err := ctx.Err(); err != nil {
			return PreparedText{}, err
		}
		prefix := space.PassagePrefix + contextText
		if section.path != "" {
			prefix += "\n" + normalizeSection(section.path)
		}
		prefix = strings.TrimRight(prefix, "\n")
		if utf8.RuneCountInString(prefix) > ChunkRunes {
			return PreparedText{}, failure(knowl.RetrievalInputLimit)
		}
		if len(section.blocks) == 0 {
			if prefix != "" {
				if err := appendPageInput(&result, prefix, TextWindow{Start: section.heading.start, End: section.heading.end}); err != nil {
					return PreparedText{}, err
				}
			}
			continue
		}
		var pending string
		window := TextWindow{}
		flush := func() error {
			if pending == "" {
				return nil
			}
			if err := appendPageInput(&result, prefix+"\n"+pending, window); err != nil {
				return err
			}
			pending = ""
			return nil
		}
		for _, block := range section.blocks {
			part := block
			value := normalizeSection(page.Body[part.start:part.end])
			if value == "" {
				continue
			}
			remaining := ChunkRunes - utf8.RuneCountInString(prefix) - 1
			if remaining < 1 {
				return PreparedText{}, failure(knowl.RetrievalInputLimit)
			}
			if utf8.RuneCountInString(value) > remaining {
				if err := flush(); err != nil {
					return PreparedText{}, err
				}
				for _, sub := range splitSourceBlockToRunes(page.Body, part, remaining) {
					if err := appendPageInput(&result, prefix+"\n"+normalizeSection(page.Body[sub.start:sub.end]), TextWindow{Start: sub.start, End: sub.end}); err != nil {
						return PreparedText{}, err
					}
				}
				continue
			}
			separator := ""
			if pending != "" {
				separator = "\n"
			}
			if utf8.RuneCountInString(pending+separator+value) > remaining {
				if err := flush(); err != nil {
					return PreparedText{}, err
				}
				separator = ""
			}
			if pending == "" {
				window.Start = part.start
			}
			pending += separator + value
			window.End = part.end
		}
		if err := flush(); err != nil {
			return PreparedText{}, err
		}
	}
	if len(result.Inputs) == 0 && contextText != "" {
		if err := appendPageInput(&result, space.PassagePrefix+contextText, TextWindow{}); err != nil {
			return PreparedText{}, err
		}
	}
	return result, nil
}

func appendPageInput(result *PreparedText, input string, window TextWindow) error {
	if len(result.Inputs) == MaxChunks {
		return failure(knowl.RetrievalProjectionCapacity)
	}
	result.Inputs = append(result.Inputs, input)
	result.Windows = append(result.Windows, window)
	return nil
}

func normalizeSection(text string) string {
	return norm.NFC.String(strings.ReplaceAll(text, "\r\n", "\n"))
}

func parseSections(body string) []sourceSection {
	root := parser.New().Parse([]byte(body))
	sections := []sourceSection{{}}
	var headings []string
	var levels []int
	var headingSpans []sourceBlock
	for node := root.FirstChild(); node != nil; node = node.NextSibling() {
		start := node.Pos()
		if start < 0 || start >= len(body) {
			continue
		}
		start = strings.LastIndexByte(body[:start], '\n') + 1
		end := len(body)
		if next := node.NextSibling(); next != nil && next.Pos() > start {
			end = strings.LastIndexByte(body[:next.Pos()], '\n') + 1
		}
		if heading, ok := node.(*ast.Heading); ok {
			for len(levels) > 0 && levels[len(levels)-1] >= heading.Level {
				levels, headings = levels[:len(levels)-1], headings[:len(headings)-1]
				headingSpans = headingSpans[:len(headingSpans)-1]
			}
			lineEnd := strings.IndexByte(body[start:end], '\n')
			if lineEnd < 0 {
				lineEnd = end - start
			}
			if lineEnd > 0 && body[start+lineEnd-1] == '\r' {
				lineEnd--
			}
			name := strings.TrimSpace(strings.Trim(body[start:start+lineEnd], "# \r\t"))
			levels = append(levels, heading.Level)
			headings = append(headings, name)
			span := sourceBlock{start, start + lineEnd}
			headingSpans = append(headingSpans, span)
			sections = append(sections, sourceSection{path: strings.Join(headings, " / "), heading: span, ancestry: append([]sourceBlock(nil), headingSpans...)})
			continue
		}
		end = trimBlockEnd(body, start, end)
		if end > start {
			sections[len(sections)-1].blocks = append(sections[len(sections)-1].blocks, sourceBlock{start, end})
		}
	}
	if len(sections) > 1 && len(sections[0].blocks) == 0 {
		sections = sections[1:]
	}
	return sections
}

func trimBlockEnd(body string, start, end int) int {
	for end > start && (body[end-1] == '\n' || body[end-1] == '\r') {
		end--
	}
	return end
}

func splitSourceBlockToRunes(body string, block sourceBlock, limit int) []sourceBlock {
	var parts []sourceBlock
	for start := block.start; start < block.end; {
		end := start
		count := 0
		lastLine := 0
		for end < block.end {
			_, size := utf8.DecodeRuneInString(body[end:block.end])
			if count+1 > limit {
				break
			}
			end += size
			count++
			if body[end-1] == '\n' {
				lastLine = end
			}
		}
		if end < block.end && lastLine > start {
			end = lastLine
		}
		parts = append(parts, sourceBlock{start, end})
		start = end
	}
	return parts
}
