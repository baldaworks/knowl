package fs

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"gopkg.in/yaml.v3"
)

func TestOperatorSnapshotDetectsReplacement(t *testing.T) {
	for _, catalog := range []bool{false, true} {
		t.Run(fmt.Sprint(catalog), func(t *testing.T) {
			workspace := newSourceStageWorkspace(t)
			reader := operatorReader(t, workspace)
			target := "wiki/entities/a.md"
			content := validWorkspacePage(operatorTestPageID, "A", testWorkspaceSourceRef, "Body")
			if catalog {
				target = "wiki/index.md"
				content = []byte("# Root\n")
			}
			writeCanonicalFixture(t, workspace, target, content)
			options := app.OperatorReadOptions{Limit: 1}
			version := func() (string, error) {
				if catalog {
					result, err := reader.CatalogChildren(t.Context(), testScope, "", options)
					return result.Children.SnapshotVersion, err
				}
				result, err := reader.PageSummaries(t.Context(), testScope, options)
				return result.SnapshotVersion, err
			}
			before, err := version()
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(workspace.root, filepath.FromSlash(target))
			info, err := os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			replacement := name + ".replacement"
			if err := os.WriteFile(replacement, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, name); err != nil {
				t.Fatal(err)
			}
			options.Continuation.SnapshotVersion = before
			if _, err := version(); !errors.Is(err, app.ErrOperatorSnapshotChanged) {
				t.Fatalf("same-metadata replacement = %v", err)
			}
		})
	}
}

func TestOperatorPaginationOrdersPageIDs(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	reader := operatorReader(t, workspace)
	for _, id := range []string{operatorTestPageID, "entities/a.b", "entities/a/z"} {
		writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, testWorkspaceSourceRef, "Body"))
	}
	options := app.OperatorReadOptions{Limit: 1}
	var ids []knowl.PageID
	for range 4 {
		result, err := reader.PageSummaries(t.Context(), testScope, options)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Items) != 1 {
			t.Fatalf("window = %#v", result)
		}
		ids = append(ids, result.Items[0].ID)
		if result.NextKey == "" {
			break
		}
		options.Continuation = app.OperatorContinuation{Key: result.NextKey, SnapshotVersion: result.SnapshotVersion}
	}
	if !reflect.DeepEqual(ids, []knowl.PageID{operatorTestPageID, "entities/a.b", "entities/a/z"}) {
		t.Fatalf("ordered pages = %#v", ids)
	}
}

func TestOperatorPublicationGuard(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	reader := operatorReader(t, workspace)
	target := "wiki/sources/engineering/operator.md"
	before := sourcePage("sources/engineering/operator", operatorPageKind, "v1")
	after := sourcePage("sources/engineering/operator", operatorPageKind, "v2")
	writeCanonicalFixture(t, workspace, target, before)
	staged, err := workspace.StageSourcePlan(t.Context(), sourcePlan("operator-prepared", testSourceID, knowl.SourceMutation{Action: knowl.SourceMutationWrite, Path: target, ExpectedDigest: digestBytes(before), Content: after}))
	if err != nil {
		t.Fatal(err)
	}
	workspace.commitFault = func(at string, _ int) error {
		if at == commitFaultApplied {
			return errInjectedCommitFault
		}
		return nil
	}
	if _, err := workspace.CommitSource(t.Context(), staged); !errors.Is(err, errInjectedCommitFault) {
		t.Fatal(err)
	}
	for _, read := range []func() error{
		func() error {
			_, err := reader.Page(t.Context(), testScope, "sources/engineering/operator", knowl.ReadLimits{})
			return err
		},
		func() error {
			_, err := reader.PageSummaries(t.Context(), testScope, app.OperatorReadOptions{Limit: 1})
			return err
		},
		func() error {
			_, err := reader.CatalogChildren(t.Context(), testScope, "", app.OperatorReadOptions{Limit: 1})
			return err
		},
		func() error {
			_, err := reader.SourceRevision(t.Context(), testScope, testWorkspaceSourceRef, knowl.ReadLimits{})
			return err
		},
	} {
		if err := read(); !errors.Is(err, app.ErrOperatorWorkspaceUnavailable) {
			t.Errorf("unresolved publication = %v", err)
		}
	}
	content, err := os.ReadFile(filepath.Join(workspace.root, filepath.FromSlash(target)))
	if err != nil || !bytes.Equal(content, after) {
		t.Fatalf("reader altered pending bytes: %v", err)
	}
}

func TestOperatorRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{operatorPageKind, workspaceRawDir, "manifest"} {
		t.Run(kind, func(t *testing.T) {
			workspace := newSourceStageWorkspace(t)
			reader := operatorReader(t, workspace)
			source := operatorAcceptSource(t, workspace, testScope, "v1", operatorPlainText, "original")
			name := "wiki/entities/a.md"
			writeCanonicalFixture(t, workspace, name, validWorkspacePage(operatorTestPageID, "A", sourceRefKey(source), "Body"))
			if kind == workspaceRawDir {
				name = filepath.ToSlash(filepath.Join(filepath.Dir(source.ManifestRef), "source"))
			}
			if kind == "manifest" {
				name = source.ManifestRef
			}
			file := filepath.Join(workspace.root, filepath.FromSlash(name))
			saved := file + ".saved"
			if err := os.Rename(file, saved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(saved, file); err != nil {
				t.Fatal(err)
			}
			var err error
			if kind == operatorPageKind {
				_, err = reader.Page(t.Context(), testScope, operatorTestPageID, knowl.ReadLimits{})
			} else {
				_, err = reader.SourceRevision(t.Context(), testScope, sourceRefKey(source), knowl.ReadLimits{})
			}
			if !errors.Is(err, app.ErrOperatorWorkspaceUnavailable) {
				t.Fatalf("unsafe %s = %v", kind, err)
			}
		})
	}
}

func TestOperatorVerifiesAcceptedIdentityAndDigest(t *testing.T) {
	for _, kind := range []string{operatorTestDigestCase, "identity"} {
		t.Run(kind, func(t *testing.T) {
			workspace := newSourceStageWorkspace(t)
			reader := operatorReader(t, workspace)
			source := operatorAcceptSource(t, workspace, testScope, "v1", operatorPlainText, "original")
			if kind == operatorTestDigestCase {
				writeCanonicalFixture(t, workspace, filepath.ToSlash(filepath.Join(filepath.Dir(source.ManifestRef), "source")), []byte("replaced"))
			} else {
				name := filepath.Join(workspace.root, filepath.FromSlash(source.ManifestRef))
				manifest, err := readManifest(name)
				if err != nil {
					t.Fatal(err)
				}
				manifest.ID = "different-document"
				data, err := yaml.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := reader.SourceRevision(t.Context(), testScope, sourceRefKey(source), knowl.ReadLimits{})
			if !errors.Is(err, app.ErrOperatorWorkspaceUnavailable) {
				t.Fatalf("replaced %s = %v", kind, err)
			}
			if kind == operatorTestDigestCase && !errors.Is(err, ErrDigestMismatch) {
				t.Fatalf("digest sentinel = %v", err)
			}
		})
	}
}

func TestOperatorDoesNotFetchSourceURI(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	workspace := newSourceStageWorkspace(t)
	reader := operatorReader(t, workspace)
	content := []byte("saved")
	source, err := workspace.AcceptSource(t.Context(), knowl.SourceEnvelope{Scope: testScope, Source: knowl.SourceRef{Adapter: testFixtureAdapter, ID: "network"}, Version: knowl.SourceVersion{Version: "v1", Digest: digestBytes(content)}, MediaType: operatorPlainText, Content: content, SourceDocument: knowl.SourceDocument{SourceID: testSourceID, DocumentID: "network", Revision: "v1", URI: server.URL + "/document?secret=key#fragment"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.SourceRevision(t.Context(), testScope, sourceRefKey(source), knowl.ReadLimits{})
	if err != nil || result.Text != "saved" || result.OriginalURI != server.URL+"/document" || calls.Load() != 0 {
		t.Fatalf("source fetch = %#v, %v, calls %d", result, err, calls.Load())
	}
}

func TestOperatorSafeURI(t *testing.T) {
	for _, tc := range []struct{ uri, want string }{
		{"https://user:password@example.test/a?key=secret#fragment", "https://example.test/a"},
		{"file:///etc/passwd", ""}, {"javascript:alert(1)", ""}, {"//example.test/a", ""}, {"https:///missing-host", ""},
	} {
		if got := operatorSafeURI(tc.uri); got != tc.want {
			t.Errorf("safe URI = %q, want %q", got, tc.want)
		}
	}
}

func TestOperatorPageTupleDuringPublication(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	reader := operatorReader(t, workspace)
	first := operatorAcceptSource(t, workspace, testScope, "v1", operatorPlainText, "one")
	second := operatorAcceptSource(t, workspace, testScope, "v2", operatorPlainText, "two")
	versions := [][]byte{validWorkspacePage(operatorTestPageID, "One", sourceRefKey(first), "One"), validWorkspacePage(operatorTestPageID, "Two", sourceRefKey(second), "Two")}
	writeCanonicalFixture(t, workspace, "wiki/entities/a.md", versions[0])
	publisher, err := New(workspace.root)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for i := range 60 {
			unlock, err := publisher.lock(t.Context())
			if err != nil {
				done <- err
				return
			}
			err = os.WriteFile(filepath.Join(workspace.root, "wiki", "entities", "a.md"), versions[i%2], 0o600)
			unlock()
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for range 60 {
		page, err := reader.Page(t.Context(), testScope, operatorTestPageID, knowl.ReadLimits{})
		if err != nil {
			t.Error(err)
			break
		}
		index := 0
		if page.Title == "Two" {
			index = 1
		}
		if page.Markdown != string(versions[index]) || page.Digest != digestBytes(versions[index]) || len(page.Sources) != 1 || page.Sources[0].Revision != []string{"v1", "v2"}[index] {
			t.Errorf("mixed publication tuple: %#v", page)
			break
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOperatorReadBoundsAndSelectedWindow(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	reader := operatorReader(t, workspace)
	writeCanonicalFixture(t, workspace, "wiki/entities/a.md", validWorkspacePage(operatorTestPageID, "A", testWorkspaceSourceRef, "Body"))
	writeCanonicalFixture(t, workspace, "wiki/entities/z.md", bytes.Repeat([]byte("x"), workspace.maxSourceBytes+1))
	first, err := reader.PageSummaries(t.Context(), testScope, app.OperatorReadOptions{Limit: 1})
	if err != nil || len(first.Items) != 1 {
		t.Fatalf("unselected page body read: %#v, %v", first, err)
	}
	_, err = reader.PageSummaries(t.Context(), testScope, app.OperatorReadOptions{Limit: 1, Continuation: app.OperatorContinuation{Key: first.NextKey, SnapshotVersion: first.SnapshotVersion}})
	if !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
		t.Fatalf("selected oversized page = %v", err)
	}
	if _, err := reader.Page(t.Context(), testScope, operatorTestPageID, knowl.ReadLimits{Characters: 1}); !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
		t.Fatalf("page rune limit = %v", err)
	}
	for _, limit := range []int{-1, 0, 101} {
		if _, err := reader.PageSummaries(t.Context(), testScope, app.OperatorReadOptions{Limit: limit}); !errors.Is(err, app.ErrOperatorLimitInvalid) {
			t.Errorf("list limit %d = %v", limit, err)
		}
	}
	writeCanonicalFixture(t, workspace, "wiki/index.md", append([]byte("# Root\n"), bytes.Repeat([]byte("text\n"), 60_000)...))
	if _, err := reader.CatalogChildren(t.Context(), testScope, "", app.OperatorReadOptions{Limit: 1}); !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
		t.Fatalf("catalog byte limit = %v", err)
	}
	source := operatorAcceptSource(t, workspace, testScope, "v1", operatorPlainText, "source")
	writeCanonicalFixture(t, workspace, source.ManifestRef, bytes.Repeat([]byte("x"), maxStageManifestBytes+1))
	if _, err := reader.SourceRevision(t.Context(), testScope, sourceRefKey(source), knowl.ReadLimits{}); !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
		t.Fatalf("manifest byte limit = %v", err)
	}
}

func TestOperatorInventoryCeiling(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	reader := operatorReader(t, workspace)
	directory := filepath.Join(workspace.root, "wiki", "assets")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	// Even irrelevant assets count toward the bounded directory walk; an
	// inventory larger than the public ceiling must not look like an empty wiki.
	for index := range 100_001 {
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("%06d", index)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reader.PageSummaries(t.Context(), testScope, app.OperatorReadOptions{Limit: 1}); !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
		t.Fatalf("inventory ceiling = %v", err)
	}
}

func TestOperatorReadsDottedPageID(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	reader := operatorReader(t, workspace)
	writeCanonicalFixture(t, workspace, "wiki/entities/dotted.name.md", validWorkspacePage("entities/dotted.name", "Dotted", testWorkspaceSourceRef, "Body"))
	page, err := reader.Page(t.Context(), testScope, "entities/dotted.name", knowl.ReadLimits{})
	if err != nil || page.Title != "Dotted" {
		t.Fatalf("dotted canonical page = %#v, %v", page, err)
	}
	writeCanonicalFixture(t, workspace, "wiki/entities/picture.png", []byte("asset"))
	if _, err := reader.Page(t.Context(), testScope, "entities/picture.png", knowl.ReadLimits{}); !errors.Is(err, app.ErrPageNotFound) {
		t.Fatalf("asset page = %v", err)
	}
}
