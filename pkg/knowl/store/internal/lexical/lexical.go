// Package lexical contains the backend-independent lexical retrieval policy.
package lexical

import (
	"errors"
	"strings"
	"unicode"
)

const (
	MaxTerms       = 32
	MaxTermRunes   = 256
	omissionMarker = "…"
)

// ErrInvalidQuery identifies a query with no usable terms or one exceeding
// the bounded lexical policy.
var ErrInvalidQuery = errors.New("invalid lexical query")

var framingWords = map[string]struct{}{
	"what": {}, "why": {}, "how": {}, "when": {}, "where": {}, "who": {},
	"which": {}, "is": {}, "are": {}, "was": {}, "were": {}, "do": {},
	"does": {}, "did": {},
}

// Query is a validated, ordered set of distinct normalized terms.
type Query struct {
	Terms []string
}

// DocumentFields contains only semantic page fields allowed to participate in
// lexical retrieval. Tags is a newline-separated list of normalized OKF tags.
type DocumentFields struct {
	Title       string
	Tags        string
	Description string
	Body        string
}

// Normalize tokenizes raw text into a bounded, first-seen sequence of distinct
// lower-case Unicode terms. Only the version-1 question framing words are
// removed.
func Normalize(raw string) (Query, error) {
	tokens := scanTokens(raw)
	terms := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	totalRunes := 0
	for _, token := range tokens {
		if _, framing := framingWords[token.value]; framing {
			continue
		}
		if _, duplicate := seen[token.value]; duplicate {
			continue
		}
		seen[token.value] = struct{}{}
		terms = append(terms, token.value)
		totalRunes += len([]rune(token.value))
		if len(terms) > MaxTerms || totalRunes > MaxTermRunes {
			return Query{}, ErrInvalidQuery
		}
	}
	if len(terms) == 0 {
		return Query{}, ErrInvalidQuery
	}
	return Query{Terms: terms}, nil
}

// Summarize builds maintenance terms from priority-ordered parts. It shares
// Normalize's lexical semantics but truncates at the fixed bounds instead of
// rejecting an already-accepted durable source.
func Summarize(parts ...string) Query {
	terms := make([]string, 0, MaxTerms)
	seen := make(map[string]struct{}, MaxTerms)
	totalRunes := 0
	for _, part := range parts {
		for _, token := range scanTokens(part) {
			if _, framing := framingWords[token.value]; framing {
				continue
			}
			if _, duplicate := seen[token.value]; duplicate {
				continue
			}
			termRunes := len([]rune(token.value))
			if termRunes > MaxTermRunes {
				continue
			}
			if len(terms) == MaxTerms || totalRunes+termRunes > MaxTermRunes {
				return Query{Terms: terms}
			}
			seen[token.value] = struct{}{}
			terms = append(terms, token.value)
			totalRunes += termRunes
		}
	}
	return Query{Terms: terms}
}

// Excerpt returns a deterministic fragment selected around the first complete
// normalized term. The native fragment is preferred when it contains a term;
// otherwise title and body are searched as the authoritative fallback.
func Excerpt(nativeFragment, title, body string, terms []string, maxRunes int) string {
	if maxRunes <= 0 {
		return body
	}
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
	if maxRunes <= 0 {
		return evidence
	}
	return Excerpt(evidence, "", "", terms, maxRunes)
}

// ContainsTerm reports whether text contains a complete normalized term token.
func ContainsTerm(text string, terms []string) bool {
	_, _, matched := firstMatch(text, terms)
	return matched
}

// Relevant reports whether a candidate matches enough distinct query terms to
// be useful. Two-term queries retain relaxed single-term recall; longer
// queries must match at least half their terms, with a floor of two.
func Relevant(text string, terms []string) bool {
	if len(terms) == 0 {
		return false
	}
	minimum := 1
	if len(terms) > 2 {
		minimum = max(2, (len(terms)+1)/2)
	}
	wanted := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		wanted[strings.ToLower(term)] = struct{}{}
	}
	matched := make(map[string]struct{}, minimum)
	for _, candidate := range scanTokens(text) {
		if _, ok := wanted[candidate.value]; !ok {
			continue
		}
		matched[candidate.value] = struct{}{}
		if len(matched) >= minimum {
			return true
		}
	}
	return false
}

// RelevantFields applies the shared relevance threshold to the complete
// allowlisted semantic search document.
func RelevantFields(fields DocumentFields, terms []string) bool {
	return Relevant(fields.Title+"\n"+fields.Tags+"\n"+fields.Description+"\n"+fields.Body, terms)
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

func scanTokens(text string) []token {
	runes := []rune(text)
	var tokens []token
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		tokens = append(tokens, token{
			value: strings.ToLower(string(runes[start:end])),
			start: start,
			end:   end,
		})
		start = -1
	}
	for index, character := range runes {
		switch {
		case unicode.IsLetter(character) || unicode.IsNumber(character):
			if start < 0 {
				start = index
			}
		case unicode.IsMark(character) && start >= 0:
			// Combining marks remain attached to an already-started token.
		default:
			flush(index)
		}
	}
	flush(len(runes))
	return tokens
}

func firstMatch(text string, terms []string) (int, int, bool) {
	if len(terms) == 0 {
		return 0, 0, false
	}
	wanted := make(map[string]struct{}, len(terms))
	for _, term := range terms {
		wanted[strings.ToLower(term)] = struct{}{}
	}
	for _, candidate := range scanTokens(text) {
		if _, ok := wanted[candidate.value]; ok {
			return candidate.start, candidate.end, true
		}
	}
	return 0, 0, false
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
