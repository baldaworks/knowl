package sourcedoclist

import (
	"encoding/json"
	"strings"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestAcceptedRevisionValidatesStoredProvenance(t *testing.T) {
	const acceptedRevision = "revision-one"
	accepted := knowl.AcceptedSource{Scope: "fixture", Source: knowl.SourceRef{Adapter: "filesystem", ID: "owner/document.md"}, Version: knowl.SourceVersion{Version: acceptedRevision, Digest: strings.Repeat("a", 64)}, SourceDocument: knowl.SourceDocument{SourceID: "owner", DocumentID: "document.md", Revision: acceptedRevision, URI: "file:///private/uri-canary"}, ManifestRef: "raw/private-manifest-canary"}
	for _, test := range []struct {
		name   string
		change func(*knowl.AcceptedSource)
		want   string
	}{
		{name: "valid", want: acceptedRevision},
		{name: "legacy provenance", change: func(a *knowl.AcceptedSource) { a.SourceDocument = knowl.SourceDocument{} }, want: acceptedRevision},
		{name: "missing adapter", change: func(a *knowl.AcceptedSource) { a.Source.Adapter = "" }},
		{name: "missing source identity", change: func(a *knowl.AcceptedSource) { a.Source.ID = "" }},
		{name: "missing digest", change: func(a *knowl.AcceptedSource) { a.Version.Digest = "" }},
		{name: "missing manifest", change: func(a *knowl.AcceptedSource) { a.ManifestRef = "" }},
		{name: "foreign scope", change: func(a *knowl.AcceptedSource) { a.Scope = "foreign" }},
		{name: "foreign owner", change: func(a *knowl.AcceptedSource) { a.SourceDocument.SourceID = "foreign" }},
		{name: "foreign document", change: func(a *knowl.AcceptedSource) { a.SourceDocument.DocumentID = "other.md" }},
		{name: "different version", change: func(a *knowl.AcceptedSource) { a.Version.Version = "revision-two" }},
		{name: "different provenance revision", change: func(a *knowl.AcceptedSource) { a.SourceDocument.Revision = "revision-two" }},
		{name: "invalid provenance URI", change: func(a *knowl.AcceptedSource) { a.SourceDocument.URI = "relative/path" }},
		{name: "oversized metadata", change: func(a *knowl.AcceptedSource) { a.ManifestRef = strings.Repeat("a", 65536) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := accepted
			if test.change != nil {
				test.change(&value)
			}
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if got := AcceptedRevision(string(raw), "fixture", "owner", "document.md", acceptedRevision); got != test.want {
				t.Fatalf("accepted revision=%q want=%q", got, test.want)
			}
		})
	}
	raw, err := json.Marshal(accepted)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "malformed", string(raw) + ` {}`, `{"scope":"fixture","unknown":"private-canary"}`} {
		if got := AcceptedRevision(value, "fixture", "owner", "document.md", acceptedRevision); got != "" {
			t.Fatalf("invalid legacy metadata acquired revision=%q", got)
		}
	}
}
