package lexical

import (
	"context"
	"encoding/base32"
	"errors"
	"strings"
	"unicode/utf8"
)

var ErrInvalidProjection = errors.New("invalid lexical projection")

const (
	MaxFieldBytes = 4 * 1024 * 1024
	MaxIndexBytes = 512 * 1024
	MaxIndexWords = 8192
)

var indexEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// EncodeFields returns transient native-index tokens while preserving input.
// It rejects an entire page on overflow and never returns partial fields.
func EncodeFields(ctx context.Context, fields DocumentFields) (DocumentFields, error) {
	if err := ctx.Err(); err != nil {
		return DocumentFields{}, err
	}
	input := []string{fields.Title, fields.Tags, fields.Description, fields.Body}
	for _, field := range input {
		if len(field) > MaxFieldBytes || !utf8.ValidString(field) {
			return DocumentFields{}, ErrInvalidProjection
		}
	}
	var out DocumentFields
	output := []*string{&out.Title, &out.Tags, &out.Description, &out.Body}
	seen := make(map[string]struct{})
	total := 0
	for i, field := range input {
		var builder strings.Builder
		var invalid bool
		err := walkTokens(ctx, field, func(word token) bool {
			if utf8.RuneCountInString(word.value) > MaxTermRunes {
				return true
			}
			if _, exists := seen[word.value]; !exists {
				if len(seen) == MaxIndexWords {
					invalid = true
					return false
				}
				seen[word.value] = struct{}{}
			}
			size := 1 + indexEncoding.EncodedLen(len(word.value))
			if builder.Len() > 0 {
				size++
			}
			if size > MaxIndexBytes-total {
				invalid = true
				return false
			}
			total += size
			if builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			builder.WriteString(encodeTerm(word.value))
			return true
		})
		if err != nil {
			return DocumentFields{}, err
		}
		if invalid {
			return DocumentFields{}, ErrInvalidProjection
		}
		*output[i] = builder.String()
	}
	if err := ctx.Err(); err != nil {
		return DocumentFields{}, err
	}
	return out, nil
}

// IndexTerms encodes validated human terms only for native retrieval.
func (q Query) IndexTerms() []string {
	encoded := make([]string, len(q.Terms))
	for i, term := range q.Terms {
		encoded[i] = encodeTerm(term)
	}
	return encoded
}

func encodeTerm(term string) string { return "k" + indexEncoding.EncodeToString([]byte(term)) }
