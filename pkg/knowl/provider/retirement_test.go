package provider

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestRuntimeMaintainerPlansBoundedRetirementRequest(t *testing.T) {
	input := testRetirementInput()
	wire := struct {
		SchemaDigest string   `json:"schema_digest"`
		Delete       bool     `json:"delete"`
		SourceRefs   []string `json:"source_refs"`
		Pages        []struct {
			Path           string `json:"path"`
			ExpectedDigest string `json:"expected_digest"`
			Content        string `json:"content"`
		} `json:"pages"`
	}{SchemaDigest: input.Schema.Digest, SourceRefs: []string{input.Evidence[0].SourceRef}}
	wire.Pages = append(wire.Pages, struct {
		Path           string `json:"path"`
		ExpectedDigest string `json:"expected_digest"`
		Content        string `json:"content"`
	}{Path: input.Page.Path, ExpectedDigest: input.Page.Digest, Content: input.Page.Content})
	output, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var captured string
	m, err := newRuntimeMaintainer(&fakeRuntimeFactory{agent: newCapturingOutputAgent(t, &captured, string(output))}, providerFailureClass, t.TempDir(), runtimeMaintainerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if _, ok := any(m).(app.RetirementMaintainer); !ok {
		t.Fatal("runtime maintainer does not expose retirement capability")
	}
	if _, ok := any(m).(app.RetirementRequestSizer); !ok {
		t.Fatal("runtime maintainer does not size its retirement wrapper")
	}

	size, err := m.RetirementRequestBytes(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := m.PlanRetirement(t.Context(), input)
	if err != nil {
		t.Fatalf("PlanRetirement() error = %v", err)
	}
	want := knowl.RetirementModelPlan{
		SchemaDigest: input.Schema.Digest, SourceRefs: []string{input.Evidence[0].SourceRef},
		Pages: []knowl.FileEdit{{Path: input.Page.Path, ExpectedDigest: input.Page.Digest, Content: []byte(input.Page.Content)}},
	}
	if !reflect.DeepEqual(plan, want) || size != len(captured) {
		t.Fatalf("retirement plan/size = %#v / %d, want %#v / %d", plan, size, want, len(captured))
	}
	var envelope struct {
		Operation         string                `json:"operation"`
		Input             knowl.RetirementInput `json:"input"`
		AllowedSourceRefs []string              `json:"allowed_source_refs"`
	}
	decoded := false
	for line := range strings.Lines(captured) {
		if json.Unmarshal([]byte(line), &envelope) == nil && envelope.Operation != "" {
			decoded = true
			break
		}
	}
	if !decoded || envelope.Operation != "source_retirement" || !reflect.DeepEqual(envelope.Input, input) || !reflect.DeepEqual(envelope.AllowedSourceRefs, []string{input.Evidence[0].SourceRef}) {
		t.Fatalf("retirement envelope = %#v, decoded=%t", envelope, decoded)
	}
}

func testRetirementInput() knowl.RetirementInput {
	active := knowl.AcceptedSource{
		Scope: baselineScope, Source: knowl.SourceRef{Adapter: "git", ID: "new/doc"},
		Version: knowl.SourceVersion{Version: "v2", Digest: strings.Repeat("a", 64)}, MediaType: "text/markdown",
		SourceDocument: knowl.SourceDocument{SourceID: "new", DocumentID: "doc", Revision: "v2", URI: "https://example.invalid/doc"},
		ManifestRef:    "raw/manifest.json",
	}
	ref := app.SourceRefKey(active)
	return knowl.RetirementInput{
		ContractVersion: app.RetirementMaintenanceContractVersion,
		InputLimits:     knowl.MaintenanceInputLimits{MaxRequestBytes: app.MaxMaintenanceRequestBytes},
		CatalogLimits:   app.DefaultCatalogLimits(),
		Scope:           baselineScope,
		Schema:          knowl.SchemaDocument{Scope: baselineScope, Digest: strings.Repeat("b", 64), Content: []byte("schema")},
		Page: knowl.PageSnapshot{
			ID: "concepts/mixed", Path: "wiki/concepts/mixed.md", Digest: strings.Repeat("c", 64),
			Content: "---\ntype: topic\ntitle: Mixed\nknowl:\n  id: concepts/mixed\n  source_refs:\n    - " + ref + "\n---\n# Mixed\n", SourceRefs: []string{ref},
		},
		Evidence: []knowl.RetirementEvidence{{SourceRef: ref, Source: active, Text: "active evidence"}},
		Catalogs: []knowl.HierarchyCatalog{{Path: "wiki/index.md", Digest: strings.Repeat("d", 64), Title: "Knowledge", Children: []string{"wiki/concepts/mixed.md"}}},
		Limits:   app.DefaultReadLimits(),
	}
}
