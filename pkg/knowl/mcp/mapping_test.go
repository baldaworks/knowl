package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestRetrieveResultMapsOptionalSourceEvidence(t *testing.T) {
	t.Parallel()
	document := &knowl.SourceDocument{SourceID: "engineering", DocumentID: "docs/one.md", Revision: "revision-1", URI: "file:///engineering/docs/one.md"}
	result := retrieveResult(app.QueryResult{Query: "evidence", Pages: []knowl.PageReference{
		{ID: "curated", Title: "Curated", Snippet: "curated", Untrusted: true},
		{ID: "sourced", Title: "Sourced", Snippet: "sourced", SourceDocument: document, Untrusted: true},
	}})
	if len(result.Evidence) != 2 || result.Evidence[0].SourceID != "" || result.Evidence[1].SourceID != "engineering" || result.Evidence[1].DocumentID != "docs/one.md" || result.Evidence[1].Revision != "revision-1" || result.Evidence[1].URI != document.URI {
		t.Fatalf("retrieve evidence = %#v", result.Evidence)
	}
	encoded, err := json.Marshal(result.Evidence[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "source_id") || strings.Contains(string(encoded), "document_id") || strings.Contains(string(encoded), "revision") || strings.Contains(string(encoded), "uri") {
		t.Fatalf("curated evidence contains source fields: %s", encoded)
	}
}
