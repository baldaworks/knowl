package lexical

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// Native backends must receive whole, reversible normalized words per field.
func TestEncodeFieldsPreservesNormalizedWords(t *testing.T) {
	original := DocumentFields{Title: "CAFÉ cafe\u0301", Tags: "ХРАНИЛИЩЕ", Description: testQuestionWord, Body: "alpha cafe"}
	got, err := EncodeFields(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}
	for i, field := range []string{got.Title, got.Tags, got.Description, got.Body} {
		var words []string
		for _, encoded := range strings.Fields(field) {
			if encoded[0] != 'k' {
				t.Fatalf("native token=%q", encoded)
			}
			decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(encoded[1:]))
			if err != nil {
				t.Fatal(err)
			}
			words = append(words, string(decoded))
		}
		want := [][]string{{testAccentWord, testAccentWord}, {"хранилище"}, {testQuestionWord}, {testAlphabeticWord, "cafe"}}[i]
		if !slices.Equal(words, want) {
			t.Fatalf("field %d decoded=%q want=%q", i, words, want)
		}
	}
	q, err := Normalize("CAFÉ ХРАНИЛИЩЕ why")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(q.IndexTerms(), []string{strings.Fields(got.Title)[0], got.Tags, got.Description}) {
		t.Fatalf("query and document identities differ")
	}
	if original.Title != "CAFÉ cafe\u0301" {
		t.Fatal("original changed")
	}
}

func TestEncodeFieldsRejectsInvalidAndCanceledInput(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		ctx    context.Context
		fields DocumentFields
		want   error
	}{
		{context.Background(), DocumentFields{Body: "bad\xff"}, ErrInvalidProjection},
		{context.Background(), DocumentFields{Tags: strings.Repeat(" ", 4*1024*1024+1)}, ErrInvalidProjection},
		{canceled, DocumentFields{Body: testAlphabeticWord}, context.Canceled},
	} {
		got, err := EncodeFields(test.ctx, test.fields)
		if !errors.Is(err, test.want) || got != (DocumentFields{}) {
			t.Fatalf("fields=%v error=%v want=%v", got, err, test.want)
		}
	}
	if _, err := EncodeFields(context.Background(), DocumentFields{Body: strings.Repeat(" ", 4*1024*1024-1) + "a"}); err != nil {
		t.Fatalf("exact raw field boundary: %v", err)
	}
}

func TestEncodeFieldsWordBounds(t *testing.T) {
	var words []string
	for i := range 8192 {
		words = append(words, fmt.Sprintf("term%d", i))
	}
	if _, err := EncodeFields(context.Background(), DocumentFields{Body: strings.Join(words, " ")}); err != nil {
		t.Fatalf("exact word boundary: %v", err)
	}
	if _, err := EncodeFields(context.Background(), DocumentFields{Body: strings.Join(words, " ") + " extra"}); !errors.Is(err, ErrInvalidProjection) {
		t.Fatalf("word overflow: %v", err)
	}
	word := strings.Repeat("𐐨", 256)
	fields, err := EncodeFields(context.Background(), DocumentFields{Body: word + " " + word + "𐐨" + " alpha"})
	if err != nil {
		t.Fatal(err)
	}
	tokens := strings.Fields(fields.Body)
	if len(tokens) != 2 || len(tokens[0]) != 1640 {
		t.Fatalf("supported maximum word encoded lengths=%v", slices.Collect(strings.SplitSeq(fields.Body, " ")))
	}
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(tokens[0][1:]))
	if err != nil || string(decoded) != word || utf8.RuneCount(decoded) != 256 {
		t.Fatalf("maximum word changed: %v", err)
	}
}

func TestEncodeFieldsAggregateByteBounds(t *testing.T) {
	// Each encoded 'a' takes three bytes and each separator takes one.
	body := strings.TrimSpace(strings.Repeat("a ", 131071))
	exact := DocumentFields{Title: "ab", Body: body}
	got, err := EncodeFields(context.Background(), exact)
	if err != nil || len(got.Title)+len(got.Body) != 512*1024 {
		t.Fatalf("exact aggregate bytes=%d error=%v", len(got.Title)+len(got.Body), err)
	}
	exact.Title = "abc"
	if got, err := EncodeFields(context.Background(), exact); !errors.Is(err, ErrInvalidProjection) || got != (DocumentFields{}) {
		t.Fatalf("one-over aggregate returned partial data or error=%v", err)
	}
}
