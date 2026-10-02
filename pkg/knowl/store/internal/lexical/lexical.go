// Package lexical contains the backend-independent lexical retrieval policy.
package lexical

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	MaxTerms            = 32
	MaxTermRunes        = 256
	MaxQueryBytes       = 64 * 1024
	DefaultSnippetRunes = 4096
	MaxSnippetRunes     = 262144
	omissionMarker      = "…"
)

// ErrInvalidQuery identifies a query with no usable terms or one exceeding
// the bounded lexical policy.
var ErrInvalidQuery = errors.New("invalid lexical query")

// Query is a validated, ordered set of distinct normalized terms.
type Query struct {
	Terms []string
}

// DocumentFields contains only semantic page fields allowed to participate in
// lexical retrieval. Tags is a newline-separated list of original OKF semantic tags.
// Values remain original until EncodeFields produces private index tokens.
type DocumentFields struct {
	Title       string
	Tags        string
	Description string
	Body        string
}

// Normalize tokenizes raw text into a bounded, first-seen sequence of distinct
// NFC/lowercase/NFC Unicode terms. Question words remain searchable.
func Normalize(raw string) (Query, error) {
	if err := ValidateQueryParts(raw); err != nil {
		return Query{}, err
	}
	query, overflow := collectTerms([]string{raw}, false)
	if overflow || len(query.Terms) == 0 {
		return Query{}, ErrInvalidQuery
	}
	return query, nil
}

// Summarize builds bounded terms in source priority order. It clips usable
// terms, while malformed or excessive raw input returns ErrInvalidQuery.
func Summarize(parts ...string) (Query, error) {
	if err := ValidateQueryParts(parts...); err != nil {
		return Query{}, err
	}
	query, _ := collectTerms(parts, true)
	return query, nil
}

func collectTerms(parts []string, clip bool) (Query, bool) {
	terms := make([]string, 0, MaxTerms)
	seen := make(map[string]struct{}, MaxTerms)
	total := 0
	overflow := false
	for _, part := range parts {
		_ = walkTokens(context.Background(), part, func(candidate token) bool {
			if _, exists := seen[candidate.value]; exists {
				return true
			}
			count := utf8.RuneCountInString(candidate.value)
			if clip && count > MaxTermRunes {
				return true
			}
			if len(terms) == MaxTerms || total+count > MaxTermRunes {
				overflow = true
				return false
			}
			seen[candidate.value] = struct{}{}
			terms = append(terms, candidate.value)
			total += count
			return true
		})
		if overflow {
			break
		}
	}
	return Query{Terms: terms}, overflow
}

// Excerpt returns a deterministic fragment selected around the first complete
// normalized term. The native fragment is preferred when it contains a term;
// otherwise title and body are searched as the authoritative fallback.
func Excerpt(nativeFragment, title, body string, terms []string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = DefaultSnippetRunes
	}
	maxRunes = min(maxRunes, MaxSnippetRunes)
	nativeFragment = strings.TrimSpace(nativeFragment)
	combined := strings.TrimSpace(title + "\n" + body)

	candidate := nativeFragment
	matchStart, matchEnd, matched := firstMatch(candidate, terms)
	if !matched {
		fallbackStart, fallbackEnd, fallbackMatched := firstMatch(combined, terms)
		if fallbackMatched || candidate == "" {
			candidate = combined
			matchStart, matchEnd, matched = fallbackStart, fallbackEnd, fallbackMatched
		}
	}
	if candidate == "" {
		return ""
	}
	characters := []rune(candidate)
	if len(characters) <= maxRunes {
		return candidate
	}
	if !matched {
		return boundedPrefix(characters, maxRunes)
	}
	return centered(characters, matchStart, matchEnd, maxRunes)
}

// ExcerptFields returns clean evidence from semantic fields. Body-native
// fragments retain priority; a tag-only match is identified explicitly without
// exposing serialized OKF or provenance metadata.
func ExcerptFields(nativeFragment string, fields DocumentFields, terms []string, maxRunes int) string {
	content := strings.TrimSpace(fields.Title + "\n" + fields.Description + "\n" + fields.Body)
	if ContainsTerm(content, terms) {
		return Excerpt(nativeFragment, fields.Title, fields.Description+"\n"+fields.Body, terms, maxRunes)
	}

	tag := matchingTag(fields.Tags, terms)
	if tag == "" {
		return Excerpt(nativeFragment, fields.Title, fields.Description+"\n"+fields.Body, terms, maxRunes)
	}
	evidence := "tag: " + tag
	context := strings.TrimSpace(fields.Description)
	if context == "" {
		context = strings.TrimSpace(fields.Title)
	}
	if context != "" {
		evidence += " — " + context
	}
	return Excerpt(evidence, "", "", terms, maxRunes)
}

// ContainsTerm reports whether text contains a complete normalized term token.
func ContainsTerm(text string, terms []string) bool {
	_, _, matched := firstMatch(text, terms)
	return matched
}

func matchingTag(tags string, terms []string) string {
	for tag := range strings.SplitSeq(tags, "\n") {
		tag = strings.TrimSpace(tag)
		if tag != "" && ContainsTerm(tag, terms) {
			return tag
		}
	}
	return ""
}

type token struct {
	value      string
	start, end int
}

// walkTokens keeps original rune positions and stops as soon as its consumer
// has enough terms or rejects a bound; it never collects an entire page.
func walkTokens(ctx context.Context, text string, visit func(token) bool) error {
	byteStart, runeStart, runeIndex := -1, -1, 0
	flush := func(byteEnd, runeEnd int) (bool, error) {
		if byteStart < 0 {
			return true, nil
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		value := normalizeWord(text[byteStart:byteEnd])
		if err := ctx.Err(); err != nil {
			return false, err
		}
		keepGoing := visit(token{value: value, start: runeStart, end: runeEnd})
		byteStart = -1
		return keepGoing, nil
	}
	for byteIndex, character := range text {
		if runeIndex%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		switch {
		case unicode.IsLetter(character) || unicode.IsNumber(character):
			if byteStart < 0 {
				byteStart, runeStart = byteIndex, runeIndex
			}
		case unicode.IsMark(character) && byteStart >= 0:
		default:
			keepGoing, err := flush(byteIndex, runeIndex)
			if err != nil || !keepGoing {
				return err
			}
		}
		runeIndex++
	}
	_, err := flush(len(text), runeIndex)
	if err != nil {
		return err
	}
	return ctx.Err()
}

func firstMatch(text string, terms []string) (int, int, bool) {
	if len(terms) == 0 {
		return 0, 0, false
	}
	wanted := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		wanted[normalizeWord(term)] = struct{}{}
	}
	var start, end int
	matched := false
	_ = walkTokens(context.Background(), text, func(candidate token) bool {
		if _, ok := wanted[candidate.value]; ok {
			start, end, matched = candidate.start, candidate.end, true
			return false
		}
		return true
	})
	return start, end, matched
}

func normalizeWord(text string) string {
	return norm.NFC.String(strings.ToLower(norm.NFC.String(text)))
}

// ValidateQueryParts bounds raw lexical input before scanning or clipping.
func ValidateQueryParts(parts ...string) error {
	total := 0
	for _, part := range parts {
		if len(part) > MaxQueryBytes-total || !utf8.ValidString(part) {
			return ErrInvalidQuery
		}
		total += len(part)
	}
	return nil
}

func boundedPrefix(characters []rune, limit int) string {
	if limit <= 0 {
		return ""
	}
	if limit == 1 {
		return omissionMarker
	}
	return string(characters[:limit-1]) + omissionMarker
}

func centered(characters []rune, matchStart, matchEnd, limit int) string {
	if limit <= 0 {
		return ""
	}
	matchLength := matchEnd - matchStart
	if matchLength >= limit {
		return string(characters[matchStart : matchStart+limit])
	}

	// A complete match takes precedence when the budget cannot also represent
	// both omitted sides. With one marker slot, mark the side with more omitted
	// source context (the leading side wins an exact tie).
	available := limit - matchLength
	leadingPossible, trailingPossible := matchStart > 0, matchEnd < len(characters)
	leading, trailing := false, false
	switch {
	case available >= 2 && leadingPossible && trailingPossible:
		leading, trailing = true, true
	case available >= 1 && leadingPossible && trailingPossible:
		leading = matchStart >= len(characters)-matchEnd
		trailing = !leading
	case available >= 1 && leadingPossible:
		leading = true
	case available >= 1 && trailingPossible:
		trailing = true
	}

	markers := boolInt(leading) + boolInt(trailing)
	contextBudget := limit - markers - matchLength
	before := min(contextBudget/2, matchStart)
	after := min(contextBudget-before, len(characters)-matchEnd)
	if before+after < contextBudget {
		before += min(contextBudget-before-after, matchStart-before)
	}
	start, end := matchStart-before, matchEnd+after
	return assemble(characters[start:end], leading && start > 0, trailing && end < len(characters))
}

func assemble(content []rune, leading, trailing bool) string {
	var builder strings.Builder
	if leading {
		builder.WriteString(omissionMarker)
	}
	builder.WriteString(string(content))
	if trailing {
		builder.WriteString(omissionMarker)
	}
	return builder.String()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
