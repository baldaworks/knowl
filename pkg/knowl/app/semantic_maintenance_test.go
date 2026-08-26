package app_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const sharedAtlasID knowl.PageID = "entities/atlas"
const sharedAtlasPath = "wiki/entities/atlas.md"

func TestConfiguredSourcesSynthesizeOneSemanticPage(t *testing.T) {
	ctx := context.Background()
	workspace, store, _, _ := newWorkflow(t, false, nil)
	maintainer := &sharedAtlasMaintainer{}
	service, err := app.NewIngestService(workspace, store, store, maintainer, app.IngestOptions{AutoApply: true})
	if err != nil {
		t.Fatal(err)
	}

	for _, envelope := range []knowl.SourceEnvelope{
		configuredAtlasEnvelope("engineering", "architecture.md", "revision-1", "# Atlas\n\nProject Atlas uses PostgreSQL for durable state.\n"),
		configuredAtlasEnvelope("operations", "runbook.md", "revision-1", "# Atlas\n\nProject Atlas PostgreSQL recovery requires projection rebuild.\n"),
	} {
		result, ingestErr := service.Ingest(ctx, envelope)
		if ingestErr != nil || result.Operation.Status != knowl.StatusCommitted {
			t.Fatalf("Ingest(%s) = %#v, %v", envelope.Source.ID, result, ingestErr)
		}
	}
	snapshot, err := workspace.Snapshot(ctx, testSourceScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Pages) != 1 || snapshot.Pages[0].ID != sharedAtlasID || len(snapshot.Pages[0].SourceRefs) != 2 {
		t.Fatalf("semantic synthesis = %#v", snapshot.Pages)
	}
	if strings.Contains(snapshot.Pages[0].Path, "/sources/") {
		t.Fatalf("semantic page mirrors a source path: %q", snapshot.Pages[0].Path)
	}
}

type sharedAtlasMaintainer struct{}

func (sharedAtlasMaintainer) Plan(_ context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	currentRef := app.SourceRefKey(input.Source)
	refs := []string{currentRef}
	expectedDigest := ""
	for _, page := range input.Pages {
		if page.ID != sharedAtlasID {
			continue
		}
		refs = append(refs, page.SourceRefs...)
		expectedDigest = page.Digest
	}
	sort.Strings(refs)
	refs = uniqueTestRefs(refs)
	content := fmt.Sprintf("---\nid: %s\ntitle: Project Atlas\ntype: entity\nsource_refs:\n  - %s\n---\n# Project Atlas\n\nPostgreSQL provides durable state and recovery uses projection rebuild.\n", sharedAtlasID, strings.Join(refs, "\n  - "))
	return withRootCatalog(input, knowl.ModelEditPlan{
		SchemaDigest: input.Schema.Digest, SourceRefs: refs,
		Edits:     []knowl.FileEdit{{Path: sharedAtlasPath, ExpectedDigest: expectedDigest, Content: []byte(content)}},
		Rationale: "synthesize overlapping Atlas evidence",
	}), nil
}

func configuredAtlasEnvelope(sourceID knowl.SourceID, documentID knowl.DocumentID, revision, body string) knowl.SourceEnvelope {
	content := []byte(body)
	return knowl.SourceEnvelope{
		Scope: testSourceScope, Source: knowl.SourceRef{Adapter: "wiki-filesystem", ID: string(sourceID) + "/" + string(documentID)},
		Version: knowl.SourceVersion{Version: revision, Digest: digest(content)}, MediaType: preparedDigestMediaType,
		SourceDocument: knowl.SourceDocument{
			SourceID: sourceID, DocumentID: documentID, Revision: revision,
			URI: "https://wiki.example.test/" + string(documentID),
		},
		Content: content,
	}
}
