package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestRetrieveResultMapsOptionalSourceEvidence(t *testing.T) {
	t.Parallel()
	document := &knowl.SourceDocument{SourceID: "engineering", DocumentID: "docs/one.md", Revision: "revision-1", URI: "file:///engineering/docs/one.md"}
	metadata := &okf.Metadata{
		Type: "Reference", Runtime: "python", Computation: "https://127.0.0.1:1/inert-computation",
		Executor:   &okf.Executor{Resource: "https://127.0.0.1:1/inert-executor"},
		Attester:   &okf.Attester{Resource: "https://127.0.0.1:1/inert-attester"},
		Extensions: map[string]any{"unknown_nested": map[string]any{"enabled": true}},
	}
	result := retrieveResult(app.QueryResult{Query: "evidence", Pages: []knowl.PageReference{
		{ID: "curated", Title: "Curated", Snippet: "curated", Untrusted: true},
		{ID: "sourced", Title: "Sourced", Snippet: "sourced", SourceDocument: document, OKF: metadata, Untrusted: true},
	}})
	if len(result.Evidence) != 2 || result.Evidence[0].SourceID != "" || result.Evidence[1].SourceID != "engineering" || result.Evidence[1].DocumentID != "docs/one.md" || result.Evidence[1].Revision != "revision-1" || result.Evidence[1].URI != document.URI {
		t.Fatalf("retrieve evidence = %#v", result.Evidence)
	}
	if result.Evidence[1].OKF == nil || result.Evidence[1].OKF.Executor == nil || result.Evidence[1].OKF.Attester == nil || !result.Evidence[1].Untrusted {
		t.Fatalf("MCP OKF evidence = %#v", result.Evidence[1])
	}
	encodedOKF, err := json.Marshal(result.Evidence[1].OKF)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"type":"Reference"`, `"computation":"https://127.0.0.1:1/inert-computation"`, `"unknown_nested":{"enabled":true}`} {
		if !strings.Contains(string(encodedOKF), field) {
			t.Fatalf("MCP OKF metadata %s missing %s", encodedOKF, field)
		}
	}
	encoded, err := json.Marshal(result.Evidence[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "source_id") || strings.Contains(string(encoded), "document_id") || strings.Contains(string(encoded), "revision") || strings.Contains(string(encoded), "uri") {
		t.Fatalf("curated evidence contains source fields: %s", encoded)
	}
}
