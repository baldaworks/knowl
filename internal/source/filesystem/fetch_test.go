package filesystem

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestFetchReturnsExactListedMarkdownAndAsset(t *testing.T) {
	root := t.TempDir()
	markdown := "---\ntitle: Explicit title\ntags: [one]\n---\n# Ignored heading\n\nBody\n"
	writeFixture(t, root, "nested/Page One.md", markdown)
	writeFixture(t, root, "assets/diagram.png", "PNG")
	source := fixtureSource(root, []string{allFilesPattern})
	source.Config.Filesystem.URIBase = "https://wiki.example.test/base"
	adapter := NewDefault()
	page, err := adapter.List(context.Background(), source, "")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	markdownDocument, err := adapter.Fetch(context.Background(), source, findRef(t, page.Documents, "nested/Page One.md"))
	if err != nil {
		t.Fatalf("Fetch(markdown) error = %v", err)
	}
	if string(markdownDocument.Content) != markdown || markdownDocument.Title != "Explicit title" {
		t.Fatalf("markdown document = %#v", markdownDocument)
	}
	if markdownDocument.URI != "https://wiki.example.test/base/nested/Page%20One.md" {
		t.Fatalf("markdown URI = %q", markdownDocument.URI)
	}
	if !strings.HasPrefix(markdownDocument.MediaType, "text/markdown") {
		t.Fatalf("markdown media type = %q", markdownDocument.MediaType)
	}

	assetDocument, err := adapter.Fetch(context.Background(), source, findRef(t, page.Documents, "assets/diagram.png"))
	if err != nil {
		t.Fatalf("Fetch(asset) error = %v", err)
	}
	if string(assetDocument.Content) != "PNG" || assetDocument.Title != "diagram" || assetDocument.MediaType != "image/png" {
		t.Fatalf("asset document = %#v", assetDocument)
	}
	if app.ValidateDocument(markdownDocument, int(DefaultLimits().MaxFileBytes)) != nil || app.ValidateDocument(assetDocument, int(DefaultLimits().MaxFileBytes)) != nil {
		t.Fatal("fetched document failed application validation")
	}
}

func TestFetchBuildsEscapedAbsoluteFileURI(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "space and ünicode.md", "# Heading title\n")
	source := fixtureSource(root, []string{allFilesPattern})
	adapter := NewDefault()
	page, err := adapter.List(context.Background(), source, "")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	document, err := adapter.Fetch(context.Background(), source, page.Documents[0])
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	parsed, err := url.Parse(document.URI)
	if err != nil || parsed.Scheme != "file" || !parsed.IsAbs() || parsed.User != nil || !strings.HasSuffix(parsed.Path, "space and ünicode.md") {
		t.Fatalf("file URI = %q, parsed=%#v, error=%v", document.URI, parsed, err)
	}
	if document.Title != "Heading title" {
		t.Fatalf("title = %q", document.Title)
	}
}

func TestFetchRejectsChangedMissingMismatchedAndSymlinkedTargets(t *testing.T) {
	t.Run("changed", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "page.md", "before")
		source := fixtureSource(root, []string{allFilesPattern})
		adapter := NewDefault()
		page, err := adapter.List(context.Background(), source, "")
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		writeFixture(t, root, "page.md", "after")
		if _, err := adapter.Fetch(context.Background(), source, page.Documents[0]); !errors.Is(err, ErrRevisionChanged) {
			t.Fatalf("Fetch() error = %v, want ErrRevisionChanged", err)
		}
	})

	t.Run("missing", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "page.md", "before")
		source := fixtureSource(root, []string{allFilesPattern})
		adapter := NewDefault()
		page, err := adapter.List(context.Background(), source, "")
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if err := os.Remove(filepath.Join(root, "page.md")); err != nil {
			t.Fatalf("remove page: %v", err)
		}
		_, err = adapter.Fetch(context.Background(), source, page.Documents[0])
		if !errors.Is(err, ErrDocumentNotFound) || strings.Contains(err.Error(), root) {
			t.Fatalf("Fetch() error = %v, want redacted ErrDocumentNotFound", err)
		}
	})

	t.Run("mismatched ref", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "page.md", "before")
		source := fixtureSource(root, []string{allFilesPattern})
		adapter := NewDefault()
		page, err := adapter.List(context.Background(), source, "")
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		ref := page.Documents[0]
		ref.Path = "other.md"
		if _, err := adapter.Fetch(context.Background(), source, ref); !errors.Is(err, app.ErrSourceInvalid) {
			t.Fatalf("Fetch() error = %v, want ErrSourceInvalid", err)
		}
	})

	t.Run("untrusted metadata does not select media type", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "page.md", "before")
		source := fixtureSource(root, []string{allFilesPattern})
		adapter := NewDefault()
		page, err := adapter.List(context.Background(), source, "")
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		ref := page.Documents[0]
		ref.Metadata["media_type"] = "application/x-untrusted"
		document, err := adapter.Fetch(context.Background(), source, ref)
		if err != nil {
			t.Fatalf("Fetch() error = %v", err)
		}
		if document.MediaType != "text/markdown" {
			t.Fatalf("media type = %q", document.MediaType)
		}
	})

	t.Run("symlink replacement", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "page.md", "before")
		source := fixtureSource(root, []string{allFilesPattern})
		adapter := NewDefault()
		page, err := adapter.List(context.Background(), source, "")
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		target := filepath.Join(root, "page.md")
		if err := os.Remove(target); err != nil {
			t.Fatalf("remove page: %v", err)
		}
		outside := filepath.Join(t.TempDir(), "outside.md")
		if err := os.WriteFile(outside, []byte("before"), 0o600); err != nil {
			t.Fatalf("write outside: %v", err)
		}
		if err := os.Symlink(outside, target); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := adapter.Fetch(context.Background(), source, page.Documents[0]); !errors.Is(err, ErrPathRejected) {
			t.Fatalf("Fetch() error = %v, want ErrPathRejected", err)
		}
	})
}

func TestFetchEnforcesBoundsCancellationAndURIValidation(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "page.md", "content")
	source := fixtureSource(root, []string{allFilesPattern})
	page, err := NewDefault().List(context.Background(), source, "")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	limits := DefaultLimits()
	limits.MaxFileBytes = 4
	bounded, err := New(limits)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := bounded.Fetch(context.Background(), source, page.Documents[0]); !errors.Is(err, ErrLimit) {
		t.Fatalf("bounded Fetch() error = %v, want ErrLimit", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewDefault().Fetch(ctx, source, page.Documents[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Fetch() error = %v", err)
	}

	invalidURI := source
	invalidURI.Config.Filesystem = cloneFilesystem(source.Config.Filesystem)
	invalidURI.Config.Filesystem.URIBase = "https://user:secret@example.test/base"
	if _, err := NewDefault().Fetch(context.Background(), invalidURI, page.Documents[0]); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("invalid URI Fetch() error = %v, want ErrSourceInvalid", err)
	}
}

func findRef(t *testing.T, refs []knowl.DocumentRef, path string) knowl.DocumentRef {
	t.Helper()
	for _, ref := range refs {
		if ref.Path == path {
			return ref
		}
	}
	t.Fatalf("missing ref %q in %#v", path, refs)
	return knowl.DocumentRef{}
}

func cloneFilesystem(config *knowl.FilesystemSourceConfig) *knowl.FilesystemSourceConfig {
	clone := *config
	clone.Include = append([]string(nil), config.Include...)
	return &clone
}
