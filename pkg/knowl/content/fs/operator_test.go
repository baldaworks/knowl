package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	operatorTestLogID      = "log"
	operatorTestPageID     = "entities/a"
	operatorTestRelatedID  = "entities/b"
	operatorTestDigestCase = "digest"
)

func operatorReader(t *testing.T, workspace *Workspace) app.WorkspaceReader {
	t.Helper()
	reader, ok := any(workspace).(app.WorkspaceReader)
	if !ok {
		t.Fatal("filesystem does not implement optional WorkspaceReader")
	}
	return reader
}

func TestOperatorInventory(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	reader := operatorReader(t, workspace)
	options := app.OperatorReadOptions{Limit: 1}
	empty, err := reader.PageSummaries(t.Context(), testScope, options)
	if err != nil || len(empty.Items) != 0 || len(empty.SnapshotVersion) != 64 {
		t.Fatalf("empty inventory = %#v, %v", empty, err)
	}
	root, err := reader.CatalogChildren(t.Context(), testScope, "", options)
	if err != nil || root.Parent.ID != operatorRootID || len(root.Children.Items) != 0 {
		t.Fatalf("empty catalog = %#v, %v", root, err)
	}
	for _, id := range []string{operatorTestPageID, operatorTestRelatedID} {
		writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, testWorkspaceSourceRef, "Body"))
	}
	first, err := reader.PageSummaries(t.Context(), testScope, options)
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != operatorTestPageID || first.NextKey != operatorTestPageID {
		t.Fatalf("first = %#v, %v", first, err)
	}
	repeat, err := reader.PageSummaries(t.Context(), testScope, options)
	if err != nil || first.SnapshotVersion != repeat.SnapshotVersion {
		t.Fatalf("repeat changed snapshot: %#v, %v", repeat, err)
	}
	options.Continuation = app.OperatorContinuation{Key: first.NextKey, SnapshotVersion: first.SnapshotVersion}
	second, err := reader.PageSummaries(t.Context(), testScope, options)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != operatorTestRelatedID || second.NextKey != "" {
		t.Fatalf("second = %#v, %v", second, err)
	}
	writeCanonicalFixture(t, workspace, "wiki/entities/b.md", validWorkspacePage(operatorTestRelatedID, "Changed", testWorkspaceSourceRef, "New body"))
	if _, err := reader.PageSummaries(t.Context(), testScope, options); !errors.Is(err, app.ErrOperatorSnapshotChanged) {
		t.Fatalf("stale cursor = %v", err)
	}
}

func TestOperatorCatalogDirectChildren(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	reader := operatorReader(t, workspace)
	writeCanonicalFixture(t, workspace, "wiki/entities/a.md", validWorkspacePage(operatorTestPageID, "A", testWorkspaceSourceRef, "Body"))
	writeCanonicalFixture(t, workspace, "wiki/catalogs/team/index.md", []byte("# Team\n\n* [A](../../entities/a.md)\n* [External](https://example.test/a%20b)\n"))
	writeCanonicalFixture(t, workspace, "wiki/sources/mirror.md", validWorkspacePage("sources/mirror", "Mirror", testWorkspaceSourceRef, "Body"))
	writeRootCatalogTargets(t, workspace, "catalogs/team/index.md")
	root, err := reader.CatalogChildren(t.Context(), testScope, "", app.OperatorReadOptions{Limit: 10})
	if err != nil || len(root.Children.Items) != 1 || root.Children.Items[0].ID != "catalogs/team/index" || root.Children.Items[0].Kind != "catalog" {
		t.Fatalf("root = %#v, %v", root, err)
	}
	nested, err := reader.CatalogChildren(t.Context(), testScope, "catalogs/team/index", app.OperatorReadOptions{Limit: 10})
	if err != nil || nested.Parent.Title != "Team" || len(nested.Children.Items) != 1 || nested.Children.Items[0].ID != operatorTestPageID || nested.Children.Items[0].Kind != operatorPageKind {
		t.Fatalf("nested = %#v, %v", nested, err)
	}
}

func TestOperatorPageAndImmutableSource(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	reader := operatorReader(t, workspace)
	source := operatorAcceptSource(t, workspace, testScope, "v1", operatorPlainText, "Original saved text")
	ref := sourceRefKey(source)
	body := "[Related](b.md) [[entities/b]] [[index]] [[log]] [[entities/%252e%252e/private]]\n"
	pageBytes := validWorkspacePage(operatorTestPageID, "A", ref, body)
	writeCanonicalFixture(t, workspace, "wiki/entities/a.md", pageBytes)
	writeCanonicalFixture(t, workspace, "wiki/entities/b.md", validWorkspacePage(operatorTestRelatedID, "B", ref, "Body"))
	page, err := reader.Page(t.Context(), testScope, operatorTestPageID, knowl.ReadLimits{})
	if err != nil || page.Markdown != string(pageBytes) || page.Digest != digestBytes(pageBytes) || page.Version == "" || page.Metadata == nil || page.Metadata.Type != "entity" {
		t.Fatalf("page = %#v, %v", page, err)
	}
	if !reflect.DeepEqual(page.RelatedPageIDs, []knowl.PageID{operatorTestRelatedID}) {
		t.Fatalf("related = %#v", page.RelatedPageIDs)
	}
	if len(page.Sources) != 1 || page.Sources[0].SourceRef != ref || page.Sources[0].OriginalURI != "https://example.test/document" {
		t.Fatalf("sources = %#v", page.Sources)
	}
	revision, err := reader.SourceRevision(t.Context(), testScope, ref, knowl.ReadLimits{})
	if err != nil || revision.Text != "Original saved text" || revision.Digest != source.Version.Digest || revision.OriginalURI != "https://example.test/document" {
		t.Fatalf("revision = %#v, %v", revision, err)
	}
	operatorAcceptSource(t, workspace, testScope, "v2", operatorPlainText, "Updated upstream text")
	again, err := reader.SourceRevision(t.Context(), testScope, ref, knowl.ReadLimits{})
	if err != nil || again.Text != revision.Text {
		t.Fatalf("saved revision changed: %#v, %v", again, err)
	}
	if _, err := reader.SourceRevision(t.Context(), "other", ref, knowl.ReadLimits{}); !errors.Is(err, app.ErrOperatorSourceRevisionNotFound) {
		t.Fatalf("cross-scope = %v", err)
	}
	other, err := reader.Page(t.Context(), "other", operatorTestPageID, knowl.ReadLimits{})
	if err != nil || len(other.Sources) != 0 {
		t.Fatalf("cross-scope provenance = %#v, %v", other, err)
	}
	// Holding detached data must not hold the workspace lock.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	unlock, err := workspace.lock(ctx)
	if err != nil {
		t.Fatalf("detached response retains lock: %v", err)
	}
	unlock()
}

func TestOperatorTypedFailures(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	reader := operatorReader(t, workspace)
	writeCanonicalFixture(t, workspace, "wiki/entities/a.md", validWorkspacePage(operatorTestPageID, "A", testWorkspaceSourceRef, "Body"))
	for _, id := range []knowl.PageID{"../private", "entities/%2e%2e/private", "entities/%252e%252e/private", operatorRootID, operatorTestLogID, "entities/a:stream", "entities//a"} {
		if _, err := reader.Page(t.Context(), testScope, id, knowl.ReadLimits{}); !errors.Is(err, app.ErrOperatorInvalidRequest) {
			t.Errorf("Page(%q) = %v", id, err)
		}
	}
	if _, err := reader.Page(t.Context(), testScope, "entities/missing", knowl.ReadLimits{}); !errors.Is(err, app.ErrPageNotFound) {
		t.Fatalf("missing page = %v", err)
	}
	if _, err := reader.CatalogChildren(t.Context(), testScope, "catalogs/missing/index", app.OperatorReadOptions{Limit: 1}); !errors.Is(err, app.ErrOperatorCatalogNotFound) {
		t.Fatalf("missing catalog = %v", err)
	}
	if _, err := reader.Page(t.Context(), testScope, operatorTestPageID, knowl.ReadLimits{Bytes: 1}); !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
		t.Fatalf("page bound = %v", err)
	}
	for _, ref := range []string{"../raw/source", "fixture:absent@1", testWorkspaceSourceRef} {
		_, err := reader.SourceRevision(t.Context(), testScope, ref, knowl.ReadLimits{Bytes: 1})
		want := app.ErrOperatorSourceRevisionNotFound
		if ref == "../raw/source" {
			want = app.ErrOperatorInvalidRequest
		}
		if ref == testWorkspaceSourceRef {
			want = app.ErrOperatorReadLimitExceeded
		}
		if !errors.Is(err, want) {
			t.Errorf("SourceRevision(%q) = %v, want %v", ref, err, want)
		}
	}
	binary := operatorAcceptSource(t, workspace, testScope, "binary", "image/png", "PNG")
	if _, err := reader.SourceRevision(t.Context(), testScope, sourceRefKey(binary), knowl.ReadLimits{}); !errors.Is(err, app.ErrOperatorUnsupportedFormat) {
		t.Fatalf("binary = %v", err)
	}
	raw := filepath.Join(workspace.root, filepath.FromSlash(filepath.Dir(binary.ManifestRef)), "source")
	if err := os.Remove(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.SourceRevision(t.Context(), testScope, sourceRefKey(binary), knowl.ReadLimits{}); !errors.Is(err, app.ErrOperatorSourceRevisionNotFound) {
		t.Fatalf("missing raw = %v", err)
	}
}

func TestOperatorPollingDoesNotReadRawBodies(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	reader := operatorReader(t, workspace)
	source := operatorAcceptSource(t, workspace, testScope, "v1", operatorPlainText, "original")
	writeCanonicalFixture(t, workspace, "wiki/entities/a.md", validWorkspacePage(operatorTestPageID, "A", sourceRefKey(source), "Body"))
	raw := filepath.Join(workspace.root, filepath.FromSlash(filepath.Dir(source.ManifestRef)), "source")
	file, err := os.OpenFile(raw, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(1 << 30); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.PageSummaries(t.Context(), testScope, app.OperatorReadOptions{Limit: 10}); err != nil {
		t.Fatalf("list read raw body: %v", err)
	}
	if _, err := reader.Page(t.Context(), testScope, operatorTestPageID, knowl.ReadLimits{}); err != nil {
		t.Fatalf("page read raw body: %v", err)
	}
	if _, err := reader.SourceRevision(t.Context(), testScope, sourceRefKey(source), knowl.ReadLimits{}); !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
		t.Fatalf("selected raw bound = %v", err)
	}
}

func operatorAcceptSource(t *testing.T, workspace *Workspace, scope knowl.ScopeRef, revision, media, text string) knowl.AcceptedSource {
	t.Helper()
	source, err := workspace.AcceptSource(t.Context(), knowl.SourceEnvelope{Scope: scope, Source: knowl.SourceRef{Adapter: testFixtureAdapter, ID: "document"}, Version: knowl.SourceVersion{Version: revision, Digest: digestBytes([]byte(text))}, MediaType: media, Content: []byte(text), SourceDocument: knowl.SourceDocument{SourceID: testSourceID, DocumentID: "document", Revision: revision, URI: "https://example.test/document?secret=value#fragment"}})
	if err != nil {
		t.Fatal(err)
	}
	return source
}
