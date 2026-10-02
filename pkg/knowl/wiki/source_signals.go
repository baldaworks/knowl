package wiki

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"gopkg.in/yaml.v3"
)

const (
	sourceYAMLStringTag = "!!str"
	sourceFieldRunes    = 256
	sourceListItems     = 32
	sourceSignalRunes   = 4096
	sourceSignalBytes   = 16384
)

// ErrSourceSignalsInvalid identifies source bytes that are not valid UTF-8.
var ErrSourceSignalsInvalid = errors.New("invalid source signals")

// SourceSignals extracts detached metadata and supported Markdown signals.
// It recognizes leading YAML, ATX/Setext headings and fenced/indented code;
// it does not implement full CommonMark. Callers bound source reads beforehand.
func SourceSignals(ctx context.Context, content []byte) (knowl.SourceSummary, error) {
	if err := ctx.Err(); err != nil {
		return knowl.SourceSummary{}, err
	}
	if !utf8.Valid(content) {
		return knowl.SourceSummary{}, ErrSourceSignalsInvalid
	}
	metadata, body, err := sourceFrontmatter(ctx, content)
	if err != nil {
		return knowl.SourceSummary{}, err
	}
	scan := sourceMarkdown{summary: metadata, seen: make(map[string]bool)}
	for len(body) > 0 {
		if err := ctx.Err(); err != nil {
			return knowl.SourceSummary{}, err
		}
		line, rest := sourceLine(body)
		body = rest
		scan.line(line)
	}
	scan.flushProse()
	if scan.summary.Title == "" {
		scan.summary.Title = scan.firstProse
	}
	scan.summary.Body = scan.prose.String()
	return BoundSourceSignals(scan.summary), ctx.Err()
}

// BoundSourceSignals clips semantic fields before parsing lexical query terms.
// Identity fields are preserved. Lists retain first-seen spelling/order; total
// semantic text is limited to 4096 runes and 16384 bytes, with 256-rune fields
// and at most 32 tags/headings. Body consumes the remaining priority budget.
func BoundSourceSignals(source knowl.SourceSummary) knowl.SourceSummary {
	budget := sourceTextBudget{runes: sourceSignalRunes, bytes: sourceSignalBytes}
	source.Title = budget.take(source.Title, sourceFieldRunes)
	source.Tags = budget.list(source.Tags)
	source.Headings = budget.list(source.Headings)
	source.Body = budget.take(source.Body, sourceSignalRunes)
	return source
}

type sourceTextBudget struct{ runes, bytes int }

func (budget *sourceTextBudget) take(text string, fieldRunes int) string {
	var out strings.Builder
	for _, r := range strings.TrimSpace(text) {
		size := utf8.RuneLen(r)
		if fieldRunes == 0 || budget.runes == 0 || size > budget.bytes {
			break
		}
		out.WriteRune(r)
		fieldRunes--
		budget.runes--
		budget.bytes -= size
	}
	return strings.TrimSpace(out.String())
}
func (budget *sourceTextBudget) list(values []string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, value := range values[:min(len(values), sourceListItems)] {
		// Deduplicate before consuming the shared budget.
		clipped := boundedSourceText(value, sourceFieldRunes)
		if clipped == "" || seen[clipped] {
			continue
		}
		seen[clipped] = true
		clipped = budget.take(clipped, sourceFieldRunes)
		if clipped != "" {
			result = append(result, clipped)
		}
	}
	return result
}
func boundedSourceText(text string, runes int) string {
	budget := sourceTextBudget{runes: runes, bytes: sourceSignalBytes}
	return budget.take(text, runes)
}
func sourceByteText(raw []byte, runes int) string {
	end := min(len(raw), sourceSignalBytes)
	for end < len(raw) && end > 0 && !utf8.RuneStart(raw[end]) {
		end--
	}
	return boundedSourceText(string(raw[:end]), runes)
}
func sourceLine(content []byte) ([]byte, []byte) {
	if index := bytes.IndexByte(content, '\n'); index >= 0 {
		return bytes.TrimSuffix(content[:index], []byte("\r")), content[index+1:]
	}
	return bytes.TrimSuffix(content, []byte("\r")), nil
}

func sourceFrontmatter(ctx context.Context, content []byte) (knowl.SourceSummary, []byte, error) {
	first, rest := sourceLine(content)
	if !bytes.Equal(first, []byte("---")) {
		return knowl.SourceSummary{}, content, nil
	}
	start := len(content) - len(rest)
	for len(rest) > 0 {
		if err := ctx.Err(); err != nil {
			return knowl.SourceSummary{}, nil, err
		}
		offset := len(content) - len(rest)
		line, next := sourceLine(rest)
		if bytes.Equal(line, []byte("---")) {
			metadata := sourceMetadata(content[start:offset])
			return metadata, next, ctx.Err()
		}
		rest = next
	}
	return knowl.SourceSummary{}, content, nil
}
func sourceMetadata(raw []byte) knowl.SourceSummary {
	limits := okf.DefaultLimits()
	if len(raw) > limits.MaxBytes {
		return knowl.SourceSummary{}
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var root, extra yaml.Node
	if decoder.Decode(&root) != nil || !errors.Is(decoder.Decode(&extra), io.EOF) || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return knowl.SourceSummary{}
	}
	remaining := limits.MaxNodes
	if !validSourceYAML(root.Content[0], 1, limits.MaxDepth, &remaining) {
		return knowl.SourceSummary{}
	}
	summary := knowl.SourceSummary{}
	fields := root.Content[0].Content
	for i := 0; i < len(fields); i += 2 {
		value := fields[i+1]
		switch fields[i].Value {
		case "title":
			if value.Kind != yaml.ScalarNode || value.Tag != sourceYAMLStringTag {
				return knowl.SourceSummary{}
			}
			summary.Title = boundedSourceText(value.Value, sourceFieldRunes)
		case "tags":
			if value.Kind != yaml.SequenceNode {
				return knowl.SourceSummary{}
			}
			for _, tag := range value.Content {
				if tag.Kind != yaml.ScalarNode || tag.Tag != sourceYAMLStringTag {
					return knowl.SourceSummary{}
				}
			}
			for _, tag := range value.Content[:min(len(value.Content), sourceListItems)] {
				summary.Tags = append(summary.Tags, boundedSourceText(tag.Value, sourceFieldRunes))
			}
		}
	}
	return summary
}
func validSourceYAML(node *yaml.Node, depth, maxDepth int, remaining *int) bool {
	if depth > maxDepth || *remaining == 0 || node.Kind == yaml.AliasNode {
		return false
	}
	*remaining--
	if node.Kind == yaml.MappingNode {
		keys := make(map[string]bool)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != sourceYAMLStringTag || key.Value == "<<" || keys[key.Value] {
				return false
			}
			keys[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if !validSourceYAML(child, depth+1, maxDepth, remaining) {
			return false
		}
	}
	return true
}

type sourceMarkdown struct {
	summary     knowl.SourceSummary
	seen        map[string]bool
	pending     []byte
	firstProse  string
	prose       strings.Builder
	proseRunes  int
	fence       byte
	fenceLength int
}

func (scan *sourceMarkdown) line(raw []byte) {
	line := bytes.Trim(raw, " \t")
	indented := sourceIndented(raw)
	marker, count, rest := sourceFence(line)
	if scan.fence != 0 {
		if !indented && marker == scan.fence && count >= scan.fenceLength && len(bytes.Trim(rest, " \t")) == 0 {
			scan.fence = 0
		}
		return
	}
	if indented {
		scan.flushProse()
		return
	}
	if count >= 3 && (marker != '`' || !bytes.ContainsRune(rest, '`')) {
		scan.flushProse()
		scan.fence = marker
		scan.fenceLength = count
		return
	}
	if len(line) == 0 {
		scan.flushProse()
		return
	}
	if sourceSetext(line) && len(scan.pending) > 0 {
		scan.heading(scan.pending)
		scan.pending = nil
		return
	}
	scan.flushProse()
	if heading, ok := sourceATX(line); ok {
		scan.heading(heading)
		return
	}
	if sourceRule(line) {
		return
	}
	scan.pending = line
}
func (scan *sourceMarkdown) heading(raw []byte) {
	text := sourceByteText(raw, sourceFieldRunes)
	if text == "" {
		return
	}
	if scan.summary.Title == "" {
		scan.summary.Title = text
	}
	if !scan.seen[text] && len(scan.summary.Headings) < sourceListItems {
		scan.summary.Headings = append(scan.summary.Headings, text)
		scan.seen[text] = true
	}
}
func (scan *sourceMarkdown) flushProse() {
	if len(scan.pending) == 0 {
		return
	}
	if scan.firstProse == "" {
		scan.firstProse = sourceByteText(scan.pending, sourceFieldRunes)
	}
	remaining := sourceSignalRunes - scan.proseRunes
	if scan.prose.Len() > 0 && remaining > 0 {
		scan.prose.WriteByte('\n')
		scan.proseRunes++
		remaining--
	}
	text := sourceByteText(scan.pending, remaining)
	scan.prose.WriteString(text)
	scan.proseRunes += utf8.RuneCountInString(text)
	scan.pending = nil
}
func sourceFence(line []byte) (byte, int, []byte) {
	if len(line) == 0 || line[0] != '`' && line[0] != '~' {
		return 0, 0, nil
	}
	count := 0
	for count < len(line) && line[count] == line[0] {
		count++
	}
	return line[0], count, line[count:]
}
func sourceATX(line []byte) ([]byte, bool) {
	markers := 0
	for markers < len(line) && line[markers] == '#' {
		markers++
	}
	if markers == 0 || markers > 6 || markers == len(line) || line[markers] != ' ' && line[markers] != '\t' {
		return nil, false
	}
	heading := bytes.TrimSpace(line[markers:])
	end := len(heading)
	for end > 0 && heading[end-1] == '#' {
		end--
	}
	if end < len(heading) && (end == 0 || heading[end-1] == ' ' || heading[end-1] == '\t') {
		heading = bytes.TrimSpace(heading[:end])
	}
	return heading, true
}
func sourceSetext(line []byte) bool {
	if len(line) == 0 || line[0] != '=' && line[0] != '-' {
		return false
	}
	for _, c := range line {
		if c != line[0] {
			return false
		}
	}
	return true
}
func sourceRule(line []byte) bool {
	if len(line) < 3 || line[0] != '-' && line[0] != '*' && line[0] != '_' {
		return sourceSetext(line)
	}
	count := 0
	for _, c := range line {
		if c == line[0] {
			count++
		} else if c != ' ' && c != '\t' {
			return false
		}
	}
	return count >= 3
}

func sourceIndented(raw []byte) bool {
	column := 0
	for _, c := range raw {
		switch c {
		case ' ':
			column++
		case '\t':
			column += 4 - column%4
		default:
			return false
		}
		if column >= 4 {
			return true
		}
	}
	return false
}
