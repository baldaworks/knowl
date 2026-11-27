package app

import (
	"errors"
	"strings"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	semanticMetadataPagePath = "wiki/concepts/semantic-metadata.md"
	semanticMetadataTag      = "metadata"
)

func TestMaintenancePlanValidatesSemanticMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "one tag", content: maintainedPageMarkdown("topic", "Semantic metadata", "Concise subject description.", []string{semanticMetadataTag})},
		{name: "six tags", content: maintainedPageMarkdown("topic", "Semantic metadata", "Concise subject description.", []string{"tag-a", "tag-b", "tag-c", "tag-d", "tag-e", "tag-f"})},
		{name: "missing type", content: "---\ntitle: Semantic metadata\ndescription: Concise subject description.\ntags: [metadata]\n---\nBody.\n", wantErr: true},
		{name: "blank type", content: maintainedPageMarkdown("   ", "Semantic metadata", "Concise subject description.", []string{semanticMetadataTag}), wantErr: true},
		{name: "missing title", content: "---\ntype: topic\ndescription: Concise subject description.\ntags: [metadata]\n---\nBody.\n", wantErr: true},
		{name: "blank title", content: maintainedPageMarkdown("topic", "   ", "Concise subject description.", []string{semanticMetadataTag}), wantErr: true},
		{name: "missing description", content: "---\ntype: topic\ntitle: Semantic metadata\ntags: [metadata]\n---\nBody.\n", wantErr: true},
		{name: "blank description", content: maintainedPageMarkdown("topic", "Semantic metadata", "   ", []string{semanticMetadataTag}), wantErr: true},
		{name: "zero tags", content: maintainedPageMarkdown("topic", "Semantic metadata", "Concise subject description.", nil), wantErr: true},
		{name: "seven tags", content: maintainedPageMarkdown("topic", "Semantic metadata", "Concise subject description.", []string{"tag-a", "tag-b", "tag-c", "tag-d", "tag-e", "tag-f", "tag-g"}), wantErr: true},
		{name: "blank tag", content: maintainedPageMarkdown("topic", "Semantic metadata", "Concise subject description.", []string{semanticMetadataTag, "   "}), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input, model, inspection := semanticMetadataPlanFixture(test.content)
			_, err := ValidateMaintenancePlan(t.Context(), input, model, inspection, DefaultCatalogLimits(), DefaultPlanLimits())
			if test.wantErr && !errors.Is(err, ErrPlanInvalid) {
				t.Fatalf("ValidateMaintenancePlan() error = %v, want ErrPlanInvalid", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("ValidateMaintenancePlan() error = %v", err)
			}
		})
	}
}

func TestGenericPlanDoesNotRequireSemanticMetadata(t *testing.T) {
	t.Parallel()
	input, model, _ := semanticMetadataPlanFixture("legacy content without frontmatter")
	if _, err := ValidatePlan(t.Context(), input, model, DefaultPlanLimits()); err != nil {
		t.Fatalf("ValidatePlan() error = %v, want legacy generic plan compatibility", err)
	}
}

func TestMaintenancePlanPreservesProtectedMetadata(t *testing.T) {
	t.Parallel()
	const before = `---
type: topic
title: Before
resource: assets/diagram.png
status: stable
stale_after: 2030-01-02T03:04:05Z
custom:
  retained: true
knowl:
  id: concepts/semantic-metadata
  source_refs: [fixture:source-1@1]
  source_document:
    source_id: handbook
    document_id: metadata.md
    revision: "1"
    uri: https://example.test/metadata
  vendor: retained
---
Before body.
`
	candidate := `---
type: reference
title: After
description: Updated semantic description.
tags: [metadata, maintenance]
resource: assets/diagram.png
status: stable
stale_after: 2030-01-02T03:04:05Z
custom:
  retained: true
knowl:
  id: concepts/semantic-metadata
  source_refs: [fixture:source-1@2]
  source_document:
    source_id: handbook
    document_id: metadata.md
    revision: "1"
    uri: https://example.test/metadata
  vendor: retained
---
After body.
`
	input, model, inspection := semanticMetadataPlanFixture(candidate)
	input.Pages[0].Content = before
	inspection.Snapshot.Pages[0].Content = before
	if _, err := ValidateMaintenancePlan(t.Context(), input, model, inspection, DefaultCatalogLimits(), DefaultPlanLimits()); err != nil {
		t.Fatalf("allowed semantic/source-ref replacement: %v", err)
	}

	mutations := map[string]string{
		"resource":        strings.Replace(candidate, "resource: assets/diagram.png", "resource: assets/other.png", 1),
		"status":          strings.Replace(candidate, "status: stable", "status: draft", 1),
		"top extension":   strings.Replace(candidate, "retained: true", "retained: false", 1),
		"knowl extension": strings.Replace(candidate, "vendor: retained", "vendor: changed", 1),
		"knowl id":        strings.Replace(candidate, "id: concepts/semantic-metadata", "id: concepts/other", 1),
		"source document": strings.Replace(candidate, "revision: \"1\"", "revision: \"2\"", 1),
	}
	for name, content := range mutations {
		t.Run(name, func(t *testing.T) {
			input, model, inspection := semanticMetadataPlanFixture(content)
			input.Pages[0].Content = before
			inspection.Snapshot.Pages[0].Content = before
			if _, err := ValidateMaintenancePlan(t.Context(), input, model, inspection, DefaultCatalogLimits(), DefaultPlanLimits()); !errors.Is(err, ErrPlanInvalid) {
				t.Fatalf("protected mutation error = %v, want ErrPlanInvalid", err)
			}
		})
	}
}

func semanticMetadataPlanFixture(content string) (knowl.MaintenanceInput, knowl.ModelEditPlan, knowl.WorkspaceInspection) {
	const digest = "semantic-metadata-digest"
	baseline := "---\ntype: topic\ntitle: Previous title\n---\nLegacy body.\n"
	input := knowl.MaintenanceInput{
		Scope:  fixtureScope,
		Schema: knowl.SchemaDocument{Digest: fixtureSchemaDigest},
		Source: knowl.AcceptedSource{Source: knowl.SourceRef{Adapter: fixtureAdapter, ID: fixtureOperationSourceID}, Version: knowl.SourceVersion{Version: "1"}},
		Pages:  []knowl.PageSnapshot{{Path: semanticMetadataPagePath, Digest: digest, Content: baseline}},
	}
	model := knowl.ModelEditPlan{
		SchemaDigest: input.Schema.Digest,
		SourceRefs:   []string{fixtureSourceRef},
		Edits:        []knowl.FileEdit{{Path: semanticMetadataPagePath, ExpectedDigest: digest, Content: []byte(content)}},
	}
	root := catalogSnapshot(rootCatalogPath, "# Navigation\n\n- [Semantic metadata](concepts/semantic-metadata.md)\n")
	inspection := knowl.WorkspaceInspection{
		Catalogs: []knowl.PageSnapshot{root},
		Snapshot: knowl.WorkspaceSnapshot{Pages: []knowl.PageSnapshot{{Path: semanticMetadataPagePath, Digest: digest, Content: baseline}}},
	}
	return input, model, inspection
}

func maintainedPageMarkdown(pageType, title, description string, tags []string) string {
	var content strings.Builder
	content.WriteString("---\ntype: ")
	content.WriteString(pageType)
	content.WriteString("\ntitle: ")
	content.WriteString(title)
	content.WriteString("\ndescription: ")
	content.WriteString(description)
	content.WriteString("\ntags:\n")
	for _, tag := range tags {
		content.WriteString("  - '")
		content.WriteString(tag)
		content.WriteString("'\n")
	}
	content.WriteString("---\nBody.\n")
	return content.String()
}
