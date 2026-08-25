package projectionmeta

import (
	"encoding/json"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestPageValuesAndDecodeRoundTripOKFMetadata(t *testing.T) {
	page := knowl.PageSnapshot{
		Content: "---\ntype: Reference\n---\ntechnical envelope",
		Body:    "user body",
		OKF: &okf.Metadata{
			Type:        "Reference",
			Description: "public description",
			Tags:        []string{"one"},
			Extensions:  map[string]any{"nested": map[string]any{"count": int64(2)}},
		},
	}
	format, description, body, encoded, err := PageValues(page)
	if err != nil {
		t.Fatalf("PageValues() error = %v", err)
	}
	if format != OKFFormat || description != "public description" || body != "user body" {
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
	if page.OKF.Tags[0] != "one" {
		t.Fatal("Decode() returned metadata aliased to its input")
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
