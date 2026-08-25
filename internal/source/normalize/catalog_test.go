package normalize

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	pageDocumentID    = "page.md"
	engineeringSource = "engineering"
	bodyText          = "Body"
)

func TestBuildCatalogIsDeterministicAndRepresentsAmbiguity(t *testing.T) {
	refs := []knowl.DocumentRef{
		catalogRef("other/Alpha.md", "markdown"),
		catalogRef("assets/b/diagram.png", "asset"),
		catalogRef("root/Beta.md", "markdown"),
		catalogRef("docs/Alpha.md", "markdown"),
		catalogRef("assets/a/diagram.png", "asset"),
	}
	catalog, err := BuildCatalog(refs, DefaultLimits())
	if err != nil {
		t.Fatalf("BuildCatalog() error = %v", err)
	}
	reversed := append([]knowl.DocumentRef(nil), refs...)
	slices.Reverse(reversed)
	repeated, err := BuildCatalog(reversed, DefaultLimits())
	if err != nil {
		t.Fatalf("BuildCatalog(reversed) error = %v", err)
	}
	if catalog.Digest() != repeated.Digest() || !reflect.DeepEqual(catalog.Entries(), repeated.Entries()) {
		t.Fatalf("catalog differs by input order: %#v != %#v", catalog, repeated)
	}
	if len(catalog.Digest()) != sha256.Size*2 {
		t.Fatalf("digest = %q", catalog.Digest())
	}

	if got, ok := catalog.ResolveNote("docs/Alpha.md"); !ok || got != "docs/Alpha" {
		t.Fatalf("exact note = %q, %v", got, ok)
	}
	if got, ok := catalog.ResolveNote("Alpha"); ok || got != "" {
		t.Fatalf("ambiguous note = %q, %v", got, ok)
	}
	if got, ok := catalog.ResolveNote("Beta"); !ok || got != "root/Beta" {
		t.Fatalf("unique note = %q, %v", got, ok)
	}
	if got, ok := catalog.ResolveAsset("assets/a/diagram.png"); !ok || got != "assets/a/diagram.png" {
		t.Fatalf("exact asset = %q, %v", got, ok)
	}
	if got, ok := catalog.ResolveAsset("diagram.png"); ok || got != "" {
		t.Fatalf("ambiguous asset = %q, %v", got, ok)
	}
	for _, unsafe := range []string{"../Alpha", "/Alpha", `docs\Alpha`, "docs/../Alpha"} {
		if got, ok := catalog.ResolveNote(unsafe); ok || got != "" {
			t.Fatalf("unsafe note %q = %q, %v", unsafe, got, ok)
		}
	}

	entries := catalog.Entries()
	entries[0].Path = "mutated"
	if catalog.Entries()[0].Path == "mutated" {
		t.Fatal("Entries returned mutable catalog storage")
	}
}

func TestBuildCatalogRejectsInvalidDuplicateAndBoundedInputs(t *testing.T) {
	valid := catalogRef("docs/page.md", "markdown")
	tests := []struct {
		name   string
		refs   []knowl.DocumentRef
		limits Limits
		want   error
	}{
		{name: "invalid ref", refs: []knowl.DocumentRef{{ExternalID: "../page.md", Path: "../page.md", Revision: strings.Repeat("a", 64)}}, limits: DefaultLimits(), want: ErrInvalid},
		{name: "id path mismatch", refs: []knowl.DocumentRef{{ExternalID: pageDocumentID, Path: "other.md", Revision: strings.Repeat("a", 64)}}, limits: DefaultLimits(), want: ErrInvalid},
		{name: "duplicate", refs: []knowl.DocumentRef{valid, valid}, limits: DefaultLimits(), want: ErrInvalid},
		{name: "document limit", refs: []knowl.DocumentRef{valid, catalogRef("docs/two.md", "markdown")}, limits: withDocumentLimit(1), want: ErrLimit},
		{name: "metadata limit", refs: []knowl.DocumentRef{valid}, limits: withMetadataLimit(1), want: ErrLimit},
		{name: "invalid limits", refs: nil, limits: Limits{}, want: ErrLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildCatalog(test.refs, test.limits); !errors.Is(err, test.want) {
				t.Fatalf("BuildCatalog() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRenderedFileIsImmutableAndBounded(t *testing.T) {
	content := []byte("content")
	file, err := NewRenderedFile("wiki/sources/engineering/page.md", content, DefaultLimits())
	if err != nil {
		t.Fatalf("NewRenderedFile() error = %v", err)
	}
	content[0] = 'X'
	got := file.Content()
	got[0] = 'Y'
	if string(file.Content()) != "content" || file.Digest() != textDigest("content") {
		t.Fatalf("rendered file mutated: %q %q", file.Content(), file.Digest())
	}
	limits := DefaultLimits()
	limits.MaxRenderedBytes = 1
	if _, err := NewRenderedFile("wiki/sources/engineering/page.md", []byte("large"), limits); !errors.Is(err, ErrLimit) {
		t.Fatalf("oversized file error = %v", err)
	}
	if _, err := NewRenderedFile("../page.md", []byte("x"), DefaultLimits()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe file error = %v", err)
	}
}

func TestMirrorDigestCoversEveryIdentityAndSortsFiles(t *testing.T) {
	first, err := NewRenderedFile("wiki/sources/engineering/page.md", []byte("page"), DefaultLimits())
	if err != nil {
		t.Fatalf("NewRenderedFile(first) error = %v", err)
	}
	second, err := NewRenderedFile("wiki/sources/engineering/asset.png", []byte("asset"), DefaultLimits())
	if err != nil {
		t.Fatalf("NewRenderedFile(second) error = %v", err)
	}
	identity := MirrorIdentity{
		SourceID: engineeringSource, DocumentID: pageDocumentID, Revision: textDigest("raw"),
		RawSource: acceptedSource(), CatalogDigest: textDigest("catalog"), Files: []RenderedFile{first, second},
	}
	digest, err := MirrorDigest(identity)
	if err != nil {
		t.Fatalf("MirrorDigest() error = %v", err)
	}
	identity.Files = []RenderedFile{second, first}
	reordered, err := MirrorDigest(identity)
	if err != nil {
		t.Fatalf("MirrorDigest(reordered) error = %v", err)
	}
	if digest != reordered || len(digest) != sha256.Size*2 {
		t.Fatalf("digest = %q, reordered = %q", digest, reordered)
	}

	changes := []func(*MirrorIdentity){
		func(value *MirrorIdentity) { value.SourceID = "operations" },
		func(value *MirrorIdentity) { value.DocumentID = "other.md" },
		func(value *MirrorIdentity) { value.Revision = textDigest("other raw") },
		func(value *MirrorIdentity) { value.RawSource.Version.Version = "v2" },
		func(value *MirrorIdentity) { value.CatalogDigest = textDigest("other catalog") },
		func(value *MirrorIdentity) {
			changed, newErr := NewRenderedFile(first.Path(), []byte("changed"), DefaultLimits())
			if newErr != nil {
				t.Fatalf("NewRenderedFile(changed) error = %v", newErr)
			}
			value.Files = []RenderedFile{changed, second}
		},
	}
	for index, change := range changes {
		candidate := identity
		candidate.Files = append([]RenderedFile(nil), identity.Files...)
		change(&candidate)
		changed, changedErr := MirrorDigest(candidate)
		if changedErr != nil {
			t.Fatalf("change %d error = %v", index, changedErr)
		}
		if changed == digest {
			t.Fatalf("change %d did not affect digest", index)
		}
	}
}

func TestMirrorDigestRejectsIncompleteIdentity(t *testing.T) {
	file, err := NewRenderedFile("wiki/sources/engineering/page.md", []byte("page"), DefaultLimits())
	if err != nil {
		t.Fatalf("NewRenderedFile() error = %v", err)
	}
	identity := MirrorIdentity{
		SourceID: engineeringSource, DocumentID: pageDocumentID, Revision: textDigest("raw"),
		RawSource: acceptedSource(), CatalogDigest: textDigest("catalog"), Files: []RenderedFile{file},
	}
	identity.RawSource.ManifestRef = ""
	if _, err := MirrorDigest(identity); !errors.Is(err, ErrInvalid) {
		t.Fatalf("MirrorDigest() error = %v, want ErrInvalid", err)
	}
}

func catalogRef(path, kind string) knowl.DocumentRef {
	return knowl.DocumentRef{
		ExternalID: knowl.DocumentID(path), Path: path, Revision: strings.Repeat("a", 64), Metadata: map[string]string{"kind": kind},
	}
}

func acceptedSource() knowl.AcceptedSource {
	return knowl.AcceptedSource{
		Scope: "local", Source: knowl.SourceRef{Adapter: "wiki-filesystem", ID: "engineering/page.md"},
		Version: knowl.SourceVersion{Version: "v1", Digest: textDigest("raw")}, MediaType: "text/markdown", ManifestRef: "raw/manifest.yaml",
	}
}

func textDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func withDocumentLimit(maximum int) Limits {
	limits := DefaultLimits()
	limits.MaxDocuments = maximum
	return limits
}

func withMetadataLimit(maximum int) Limits {
	limits := DefaultLimits()
	limits.MaxMetadataBytes = maximum
	return limits
}
