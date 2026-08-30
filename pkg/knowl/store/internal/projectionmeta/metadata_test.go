package projectionmeta

import (
	"encoding/json"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	testPublicDescription = "public description"
	testUserBody          = "user body"
)

func TestPageValuesAndDecodeRoundTripOKFMetadata(t *testing.T) {
	page := knowl.PageSnapshot{
		Content: "---\ntype: Reference\n---\ntechnical envelope",
		Body:    testUserBody,
		OKF: &okf.Metadata{
			Type:        "Reference",
			Description: testPublicDescription,
			Tags:        []string{" one ", "Architecture\nDecision", "ONE", "  "},
			Extensions:  map[string]any{"nested": map[string]any{"count": int64(2)}},
		},
	}
	values, err := ValuesForPage(page)
	if err != nil {
		t.Fatalf("ValuesForPage() error = %v", err)
	}
	if values.Format != OKFFormat || values.Tags != "one\nArchitecture Decision" || values.Description != testPublicDescription || values.Body != testUserBody {
		t.Fatalf("ValuesForPage() = %#v", values)
	}
	format, description, body, encoded, err := PageValues(page)
	if err != nil {
		t.Fatalf("PageValues() error = %v", err)
	}
	if format != OKFFormat || description != testPublicDescription || body != testUserBody {
		t.Fatalf("PageValues() = %q %q %q", format, description, body)
	}
	decoded, err := Decode(format, encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	reencoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("encode decoded metadata: %v", err)
	}
	if string(reencoded) != string(encoded) {
		t.Fatalf("Decode() = %#v, want %#v", decoded, page.OKF)
	}
	decoded.Tags[0] = "changed"
	if page.OKF.Tags[0] != " one " {
		t.Fatal("Decode() returned metadata aliased to its input")
	}
}

func TestValuesForPageKeepsLegacySearchFieldsClean(t *testing.T) {
	t.Parallel()
	page := knowl.PageSnapshot{
		Content: "---\nsource_refs:\n  - raw:secret@1\n---\nlegacy user body",
		Title:   "Legacy",
	}
	values, err := ValuesForPage(page)
	if err != nil {
		t.Fatalf("ValuesForPage() error = %v", err)
	}
	if values.Format != "" || values.Tags != "" || values.Description != "" || values.Metadata != nil || values.Body != "legacy user body" {
		t.Fatalf("ValuesForPage() = %#v", values)
	}
}

func TestDecodeRejectsInconsistentProjectionMetadata(t *testing.T) {
	for _, test := range []struct {
		name     string
		format   string
		metadata []byte
	}{
		{name: "missing discriminator", metadata: []byte(`{"type":"Reference"}`)},
		{name: "unknown format", format: "okf/9.9", metadata: []byte(`{"type":"Reference"}`)},
		{name: "missing metadata", format: OKFFormat},
		{name: "invalid JSON", format: OKFFormat, metadata: []byte(`{`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Decode(test.format, test.metadata); err == nil {
				t.Fatal("Decode() error = nil")
			}
		})
	}
	if decoded, err := Decode("", nil); err != nil || decoded != nil {
		t.Fatalf("Decode() legacy row = %#v, %v", decoded, err)
	}
}

func TestSemanticPageAndSourceDocuments(t *testing.T) {
	const engineering = "engineering"
	for _, pagePath := range []string{"wiki/index.md", "wiki/entities/index.md", "wiki/log.md", "wiki/sources/engineering/page.md"} {
		if SemanticPage(knowl.PageSnapshot{Path: pagePath}) {
			t.Errorf("SemanticPage(%q) = true", pagePath)
		}
	}
	if !SemanticPage(knowl.PageSnapshot{Path: "wiki/entities/page.md"}) {
		t.Fatal("semantic entity was excluded")
	}
	documents := SourceDocuments(knowl.PageSnapshot{SourceDocuments: []knowl.SourceDocument{
		{SourceID: "operations", DocumentID: "runbook.md", Revision: "1"},
		{SourceID: engineering, DocumentID: "architecture.md", Revision: "1"},
		{SourceID: engineering, DocumentID: "architecture.md", Revision: "1"},
	}})
	if len(documents) != 2 || documents[0].SourceID != engineering || documents[1].SourceID != "operations" {
		t.Fatalf("SourceDocuments() = %#v", documents)
	}
}
