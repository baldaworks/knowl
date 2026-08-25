package filesystem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const allFilesPattern = "**/*"

func TestNewEnforcesConfiguredLimitBoundaries(t *testing.T) {
	maximum := Limits{
		PageSize:         1000,
		MaxVisited:       100_000,
		MaxDocuments:     100_000,
		MaxPathBytes:     4096,
		MaxTokenBytes:    4096,
		MaxFileBytes:     64 << 20,
		MaxMetadataBytes: 64 << 20,
	}
	if _, err := New(maximum); err != nil {
		t.Fatalf("New(maximum) error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Limits)
	}{
		{name: "page size", mutate: func(limits *Limits) { limits.PageSize++ }},
		{name: "visited entries", mutate: func(limits *Limits) { limits.MaxVisited++ }},
		{name: "documents", mutate: func(limits *Limits) { limits.MaxDocuments++ }},
		{name: "path bytes", mutate: func(limits *Limits) { limits.MaxPathBytes++ }},
		{name: "token bytes", mutate: func(limits *Limits) { limits.MaxTokenBytes++ }},
		{name: "file bytes", mutate: func(limits *Limits) { limits.MaxFileBytes++ }},
		{name: "metadata bytes", mutate: func(limits *Limits) { limits.MaxMetadataBytes++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := maximum
			test.mutate(&limits)
			if _, err := New(limits); !errors.Is(err, ErrLimit) {
				t.Fatalf("New(over limit) error = %v, want ErrLimit", err)
			}
		})
	}
}

func TestListHandlesEmptyAndExactPageBoundaries(t *testing.T) {
	limits := DefaultLimits()
	limits.PageSize = 2
	adapter, err := New(limits)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	emptySource := fixtureSource(t.TempDir(), []string{allFilesPattern})
	empty, err := adapter.List(context.Background(), emptySource, "")
	if err != nil {
		t.Fatalf("List(empty) error = %v", err)
	}
	if len(empty.Documents) != 0 || empty.NextPageToken != "" {
		t.Fatalf("List(empty) = %#v", empty)
	}

	root := t.TempDir()
	writeFixture(t, root, "b.md", "b")
	writeFixture(t, root, "a.md", "a")
	source := fixtureSource(root, []string{allFilesPattern})
	exact, err := adapter.List(context.Background(), source, "")
	if err != nil {
		t.Fatalf("List(exact boundary) error = %v", err)
	}
	if got, want := refPaths(exact.Documents), []string{"a.md", "b.md"}; !reflect.DeepEqual(got, want) || exact.NextPageToken != "" {
		t.Fatalf("List(exact boundary) paths/token = %#v, %q", got, exact.NextPageToken)
	}
	repeated, err := adapter.List(context.Background(), source, "")
	if err != nil || !reflect.DeepEqual(repeated, exact) {
		t.Fatalf("List(repeated exact boundary) = %#v, %v; want %#v", repeated, err, exact)
	}
}

func TestListIsDeterministicBoundedAndPaged(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "z.md", "# Z\n")
	writeFixture(t, root, "nested/a.md", "# A\n")
	writeFixture(t, root, "nested/image.png", "PNG")
	writeFixture(t, root, ".obsidian/workspace.json", "secret")
	writeFixture(t, root, ".hidden.md", "hidden")

	limits := DefaultLimits()
	limits.PageSize = 2
	adapter, err := New(limits)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	source := fixtureSource(root, []string{allFilesPattern})

	first, err := adapter.List(context.Background(), source, "")
	if err != nil {
		t.Fatalf("first List() error = %v", err)
	}
	if got, want := refPaths(first.Documents), []string{"nested/a.md", "nested/image.png"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first paths = %#v, want %#v", got, want)
	}
	if first.NextPageToken == "" {
		t.Fatal("first NextPageToken is empty")
	}
	second, err := adapter.List(context.Background(), source, first.NextPageToken)
	if err != nil {
		t.Fatalf("second List() error = %v", err)
	}
	if got, want := refPaths(second.Documents), []string{"z.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second paths = %#v, want %#v", got, want)
	}
	if second.NextPageToken != "" {
		t.Fatalf("second NextPageToken = %q, want empty", second.NextPageToken)
	}

	repeated, err := adapter.List(context.Background(), source, "")
	if err != nil {
		t.Fatalf("repeated List() error = %v", err)
	}
	if !reflect.DeepEqual(repeated, first) {
		t.Fatalf("repeated page differs:\nfirst=%#v\nrepeated=%#v", first, repeated)
	}
	for _, ref := range append(first.Documents, second.Documents...) {
		if ref.ExternalID != knowl.DocumentID(ref.Path) || len(ref.Revision) != sha256.Size*2 {
			t.Fatalf("invalid descriptor = %#v", ref)
		}
		if ref.Metadata["kind"] == "" {
			t.Fatalf("descriptor metadata = %#v", ref.Metadata)
		}
	}
}

func TestListRevisionsTrackExactBytesWithoutFetch(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "page.md", "first")
	writeFixture(t, root, "other.md", "stable")
	adapter := NewDefault()
	source := fixtureSource(root, []string{"*.md"})

	before, err := adapter.List(context.Background(), source, "")
	if err != nil {
		t.Fatalf("List(before) error = %v", err)
	}
	if got := revisionFor(before.Documents, "page.md"); got != digest("first") {
		t.Fatalf("page revision = %q, want %q", got, digest("first"))
	}
	writeFixture(t, root, "page.md", "second")
	after, err := adapter.List(context.Background(), source, "")
	if err != nil {
		t.Fatalf("List(after) error = %v", err)
	}
	if revisionFor(after.Documents, "page.md") == revisionFor(before.Documents, "page.md") {
		t.Fatal("changed bytes retained the same revision")
	}
	if revisionFor(after.Documents, "other.md") != revisionFor(before.Documents, "other.md") {
		t.Fatal("unmodified document revision changed")
	}
}

func TestListHonorsIncludesAndRejectsInvalidOrStaleTokens(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "root.md", "root")
	writeFixture(t, root, "runbooks/nested.md", "nested")
	writeFixture(t, root, "runbooks/ignored.txt", "ignored")
	limits := DefaultLimits()
	limits.PageSize = 1
	adapter, err := New(limits)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	source := fixtureSource(root, []string{"runbooks/**/*.md"})
	page, err := adapter.List(context.Background(), source, "")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got, want := refPaths(page.Documents), []string{"runbooks/nested.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
	if _, err := adapter.List(context.Background(), source, "not-base64"); !errors.Is(err, ErrPageToken) {
		t.Fatalf("invalid token error = %v, want ErrPageToken", err)
	}

	all := fixtureSource(root, []string{"**/*.md"})
	first, err := adapter.List(context.Background(), all, "")
	if err != nil {
		t.Fatalf("paged List() error = %v", err)
	}
	if first.NextPageToken == "" {
		t.Fatal("expected page token")
	}
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(first.Documents[0].Path))); err != nil {
		t.Fatalf("remove cursor file: %v", err)
	}
	if _, err := adapter.List(context.Background(), all, first.NextPageToken); !errors.Is(err, ErrPageToken) {
		t.Fatalf("stale token error = %v, want ErrPageToken", err)
	}
}

func TestListRejectsSymlinksLimitsAndCancellation(t *testing.T) {
	t.Run("symlink directory", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		writeFixture(t, outside, "outside.md", "outside")
		if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
			t.Skipf("directory symlink unavailable: %v", err)
		}
		page, err := NewDefault().List(context.Background(), fixtureSource(root, []string{allFilesPattern}), "")
		if !errors.Is(err, ErrPathRejected) || len(page.Documents) != 0 || page.NextPageToken != "" {
			t.Fatalf("List() = %#v, %v; want empty ErrPathRejected", page, err)
		}
	})

	t.Run("symlink entry", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside.md")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatalf("write outside: %v", err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "link.md")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := NewDefault().List(context.Background(), fixtureSource(root, []string{allFilesPattern}), ""); !errors.Is(err, ErrPathRejected) {
			t.Fatalf("List() error = %v, want ErrPathRejected", err)
		}
	})

	t.Run("non-regular entry", func(t *testing.T) {
		root := t.TempDir()
		listener, err := net.Listen("unix", filepath.Join(root, "socket.asset"))
		if err != nil {
			t.Skipf("Unix socket unavailable: %v", err)
		}
		defer func() { _ = listener.Close() }()
		page, err := NewDefault().List(context.Background(), fixtureSource(root, []string{allFilesPattern}), "")
		if !errors.Is(err, ErrPathRejected) || len(page.Documents) != 0 || page.NextPageToken != "" {
			t.Fatalf("List() = %#v, %v; want empty ErrPathRejected", page, err)
		}
	})

	t.Run("invalid UTF-8 entry", func(t *testing.T) {
		root := t.TempDir()
		name := string([]byte{'b', 'a', 'd', 0xff, '.', 'm', 'd'})
		if err := os.WriteFile(filepath.Join(root, name), []byte("content-secret"), 0o600); err != nil {
			t.Skipf("invalid UTF-8 filename unavailable: %v", err)
		}
		page, err := NewDefault().List(context.Background(), fixtureSource(root, []string{allFilesPattern}), "")
		if !errors.Is(err, ErrPathRejected) || len(page.Documents) != 0 || page.NextPageToken != "" {
			t.Fatalf("List() = %#v, %v; want empty ErrPathRejected", page, err)
		}
	})

	t.Run("symlink root", func(t *testing.T) {
		realRoot := t.TempDir()
		linkedRoot := filepath.Join(t.TempDir(), "source")
		if err := os.Symlink(realRoot, linkedRoot); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := NewDefault().List(context.Background(), fixtureSource(linkedRoot, []string{allFilesPattern}), ""); !errors.Is(err, ErrPathRejected) {
			t.Fatalf("List() error = %v, want ErrPathRejected", err)
		}
	})

	t.Run("file bytes", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "large.md", "large")
		limits := DefaultLimits()
		limits.MaxFileBytes = 4
		adapter, err := New(limits)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if _, err := adapter.List(context.Background(), fixtureSource(root, []string{allFilesPattern}), ""); !errors.Is(err, ErrLimit) {
			t.Fatalf("List() error = %v, want ErrLimit", err)
		}
	})

	t.Run("visited entries", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "one.md", "one")
		limits := DefaultLimits()
		limits.MaxVisited = 1
		limits.MaxDocuments = 1
		adapter, err := New(limits)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if _, err := adapter.List(context.Background(), fixtureSource(root, []string{allFilesPattern}), ""); !errors.Is(err, ErrLimit) {
			t.Fatalf("List() error = %v, want ErrLimit", err)
		}
	})

	t.Run("document count", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "one.md", "one")
		writeFixture(t, root, "two.md", "two")
		limits := DefaultLimits()
		limits.MaxDocuments = 1
		adapter, err := New(limits)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		page, err := adapter.List(context.Background(), fixtureSource(root, []string{allFilesPattern}), "")
		if !errors.Is(err, ErrLimit) || len(page.Documents) != 0 || page.NextPageToken != "" {
			t.Fatalf("List() = %#v, %v; want empty ErrLimit", page, err)
		}
	})

	t.Run("encoded token bytes", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "one.md", "one")
		writeFixture(t, root, "two.md", "two")
		limits := DefaultLimits()
		limits.PageSize = 1
		limits.MaxTokenBytes = 1
		adapter, err := New(limits)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		page, err := adapter.List(context.Background(), fixtureSource(root, []string{allFilesPattern}), "")
		if !errors.Is(err, ErrLimit) || len(page.Documents) != 0 || page.NextPageToken != "" {
			t.Fatalf("List() = %#v, %v; want empty ErrLimit", page, err)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := NewDefault().List(ctx, fixtureSource(t.TempDir(), []string{allFilesPattern}), ""); !errors.Is(err, context.Canceled) {
			t.Fatalf("List() error = %v, want context cancellation", err)
		}
	})
}

func TestListRejectsUnnormalizedSourcesWithoutLeakingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absolute-root-secret")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create secret-shaped root: %v", err)
	}
	adapter := NewDefault()
	tests := []knowl.Source{
		fixtureSource(root, []string{allFilesPattern}),
		fixtureSource(root, nil),
		fixtureSource("relative", []string{allFilesPattern}),
		fixtureSource(root, []string{"../*.md"}),
	}
	tests[0].Enabled = false
	for _, source := range tests {
		_, err := adapter.List(context.Background(), source, "")
		if !errors.Is(err, app.ErrSourceInvalid) {
			t.Fatalf("List(%#v) error = %v, want ErrSourceInvalid", source, err)
		}
		for _, secret := range []string{root, "absolute-root-secret"} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaked %q: %v", secret, err)
			}
		}
	}
}

func TestListErrorsRedactCredentialsContentAndAbsoluteRoots(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root-password-secret")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create secret-shaped root: %v", err)
	}
	const contentSecret = "raw-content-secret"
	writeFixture(t, root, "page.md", contentSecret)

	limits := DefaultLimits()
	limits.MaxFileBytes = 1
	adapter, err := New(limits)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	page, err := adapter.List(context.Background(), fixtureSource(root, []string{allFilesPattern}), "")
	if !errors.Is(err, ErrLimit) || len(page.Documents) != 0 || page.NextPageToken != "" {
		t.Fatalf("List(content limit) = %#v, %v; want empty ErrLimit", page, err)
	}
	assertErrorRedacts(t, err, root, "root-password-secret", contentSecret)

	credentialSource := fixtureSource(root, []string{allFilesPattern})
	credentialSource.Config.Filesystem.URIBase = "https://operator:uri-password-secret@example.test/wiki"
	page, err = NewDefault().List(context.Background(), credentialSource, "")
	if !errors.Is(err, app.ErrSourceInvalid) || len(page.Documents) != 0 || page.NextPageToken != "" {
		t.Fatalf("List(credential URI) = %#v, %v; want empty ErrSourceInvalid", page, err)
	}
	assertErrorRedacts(t, err, root, "root-password-secret", "operator", "uri-password-secret", contentSecret)
}

func assertErrorRedacts(t *testing.T, err error, secrets ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, secret := range secrets {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
}

func fixtureSource(root string, includes []string) knowl.Source {
	return knowl.Source{
		ID:      "engineering",
		Type:    knowl.SourceTypeFilesystem,
		Enabled: true,
		Config: knowl.SourceConfig{Filesystem: &knowl.FilesystemSourceConfig{
			Root: root, Include: includes, Flavor: knowl.SourceFlavorMarkdown,
		}},
	}
}

func writeFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create fixture parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture %q: %v", relative, err)
	}
}

func refPaths(refs []knowl.DocumentRef) []string {
	paths := make([]string, len(refs))
	for index, ref := range refs {
		paths[index] = ref.Path
	}
	return paths
}

func revisionFor(refs []knowl.DocumentRef, path string) string {
	for _, ref := range refs {
		if ref.Path == path {
			return ref.Revision
		}
	}
	return ""
}

func digest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
