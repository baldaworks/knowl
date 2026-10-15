package app_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	sourceRefTestGitAdapter = "git"
	sourceRefTestPageID     = "engineering/page.md"
)

func TestSourceRefCodecPreservesLegacyAndRoundTripsEscapedIdentities(t *testing.T) {
	tests := []struct {
		name     string
		identity app.SourceRefIdentity
		want     string
	}{
		{
			name:     "legacy spelling remains stable",
			identity: app.SourceRefIdentity{Source: knowl.SourceRef{Adapter: sourceRefTestGitAdapter, ID: "engineering/docs/page.md"}, Revision: "v1"},
			want:     "git:engineering/docs/page.md@v1",
		},
		{
			name:     "at sign in document identity",
			identity: app.SourceRefIdentity{Source: knowl.SourceRef{Adapter: sourceRefTestGitAdapter, ID: "engineering/docs/a@b.md"}, Revision: "v1"},
			want:     "v2:git:engineering%2Fdocs%2Fa%40b.md:v1",
		},
		{
			name:     "at sign in revision",
			identity: app.SourceRefIdentity{Source: knowl.SourceRef{Adapter: sourceRefTestGitAdapter, ID: "engineering/docs/page.md"}, Revision: "v1@v2"},
			want:     "v2:git:engineering%2Fdocs%2Fpage.md:v1%40v2",
		},
		{
			name:     "percent colon and unicode are escaped canonically",
			identity: app.SourceRefIdentity{Source: knowl.SourceRef{Adapter: "git:mirror", ID: "engineering/100%@Привет.md"}, Revision: "ветка:1"},
			want:     "v2:git%3Amirror:engineering%2F100%25%40%D0%9F%D1%80%D0%B8%D0%B2%D0%B5%D1%82.md:%D0%B2%D0%B5%D1%82%D0%BA%D0%B0%3A1",
		},
		{
			name:     "v2 prefix with legacy delimiter is not canonical",
			identity: app.SourceRefIdentity{Source: knowl.SourceRef{Adapter: "v2", ID: "git:docs/page.md"}, Revision: "v1"},
			want:     "v2:git:docs/page.md@v1",
		},
		{
			name:     "inner whitespace remains reversible",
			identity: app.SourceRefIdentity{Source: knowl.SourceRef{Adapter: sourceRefTestGitAdapter, ID: "engineering/my page.md"}, Revision: "release candidate"},
			want:     "git:engineering/my page.md@release candidate",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := app.EncodeSourceRef(test.identity)
			if err != nil || got != test.want {
				t.Fatalf("EncodeSourceRef() = %q, %v; want %q", got, err, test.want)
			}
			parsed, err := app.ParseSourceRefCandidates(got)
			if err != nil || !reflect.DeepEqual(parsed, []app.SourceRefIdentity{test.identity}) {
				t.Fatalf("ParseSourceRefCandidates() = %#v, %v; want %#v", parsed, err, test.identity)
			}
		})
	}
}

func TestSourceRefCodecCanonicalizesComponentEdgeWhitespace(t *testing.T) {
	tests := []struct {
		name     string
		identity app.SourceRefIdentity
		legacy   string
		want     string
	}{
		{name: "adapter leading", identity: app.SourceRefIdentity{Source: knowl.SourceRef{Adapter: " git", ID: sourceRefTestPageID}, Revision: "v1"}, legacy: " git:engineering/page.md@v1", want: "v2:%20git:engineering%2Fpage.md:v1"},
		{name: "adapter trailing", identity: app.SourceRefIdentity{Source: knowl.SourceRef{Adapter: "git ", ID: sourceRefTestPageID}, Revision: "v1"}, legacy: "git :engineering/page.md@v1", want: "v2:git%20:engineering%2Fpage.md:v1"},
		{name: "id leading", identity: app.SourceRefIdentity{Source: knowl.SourceRef{Adapter: sourceRefTestGitAdapter, ID: " engineering/page.md"}, Revision: "v1"}, legacy: "git: engineering/page.md@v1", want: "v2:git:%20engineering%2Fpage.md:v1"},
		{name: "id trailing", identity: app.SourceRefIdentity{Source: knowl.SourceRef{Adapter: sourceRefTestGitAdapter, ID: "engineering/page.md "}, Revision: "v1"}, legacy: "git:engineering/page.md @v1", want: "v2:git:engineering%2Fpage.md%20:v1"},
		{name: "revision leading", identity: app.SourceRefIdentity{Source: knowl.SourceRef{Adapter: sourceRefTestGitAdapter, ID: sourceRefTestPageID}, Revision: " v1"}, legacy: "git:engineering/page.md@ v1", want: "v2:git:engineering%2Fpage.md:%20v1"},
		{name: "revision trailing", identity: app.SourceRefIdentity{Source: knowl.SourceRef{Adapter: sourceRefTestGitAdapter, ID: sourceRefTestPageID}, Revision: "v1 "}, legacy: "git:engineering/page.md@v1 ", want: "v2:git:engineering%2Fpage.md:v1%20"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := app.EncodeSourceRef(test.identity)
			if err != nil || encoded != test.want {
				t.Fatalf("EncodeSourceRef() = %q, %v; want %q", encoded, err, test.want)
			}
			parsed, err := app.ParseSourceRefCandidates(encoded)
			if err != nil || !reflect.DeepEqual(parsed, []app.SourceRefIdentity{test.identity}) {
				t.Fatalf("canonical ParseSourceRefCandidates() = %#v, %v; want %#v", parsed, err, test.identity)
			}
			if legacyParsed, legacyErr := app.ParseSourceRefCandidates(test.legacy); !errors.Is(legacyErr, app.ErrSourceInvalid) || len(legacyParsed) != 0 {
				t.Fatalf("legacy ParseSourceRefCandidates() = %#v, %v; want noncanonical rejection", legacyParsed, legacyErr)
			}
		})
	}
}

func TestSourceRefCodecEnumeratesLegacyDelimiterCandidates(t *testing.T) {
	got, err := app.ParseSourceRefCandidates("git:engineering/a@b@c")
	want := []app.SourceRefIdentity{
		{Source: knowl.SourceRef{Adapter: sourceRefTestGitAdapter, ID: "engineering/a"}, Revision: "b@c"},
		{Source: knowl.SourceRef{Adapter: sourceRefTestGitAdapter, ID: "engineering/a@b"}, Revision: "c"},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseSourceRefCandidates() = %#v, %v; want %#v", got, err, want)
	}
}

func TestSourceRefCodecBoundsLegacyCandidateSplits(t *testing.T) {
	exact := "git:engineering/page" + strings.Repeat("@part", app.MaxLegacySourceRefCandidates)
	candidates, err := app.ParseSourceRefCandidates(exact)
	if err != nil || len(candidates) != app.MaxLegacySourceRefCandidates {
		t.Fatalf("exact candidate cap = %d, %v; want %d", len(candidates), err, app.MaxLegacySourceRefCandidates)
	}

	over := exact + "@overflow"
	if candidates, err = app.ParseSourceRefCandidates(over); !errors.Is(err, app.ErrSourceRefLimit) || len(candidates) != 0 {
		t.Fatalf("candidate cap +1 = %d, %v; want typed limit", len(candidates), err)
	}

	prefix := "git:x"
	pathological := prefix + strings.Repeat("@", (8<<10)-len(prefix)-1) + "x"
	if candidates, err = app.ParseSourceRefCandidates(pathological); !errors.Is(err, app.ErrSourceRefLimit) || len(candidates) != 0 {
		t.Fatalf("8 KiB legacy amplification = %d, %v; want typed limit", len(candidates), err)
	}
}

func TestSourceRefCodecRejectsMalformedAndNoncanonicalRefs(t *testing.T) {
	tests := []string{
		"", "git:@v1", "git:page@", "git-page-v1", "git:page@v\n1",
		"v2:git:docs%2fpage:v1", "v2:git:docs%41:v1", "v2:git:docs%ZZ:v1",
		strings.Repeat("a", (8<<10)+1),
	}
	for _, ref := range tests {
		if candidates, err := app.ParseSourceRefCandidates(ref); err == nil || len(candidates) != 0 {
			t.Errorf("ParseSourceRefCandidates(%q) = %#v, %v; want invalid", ref, candidates, err)
		}
	}
}

func FuzzSourceRefCodecRoundTrip(f *testing.F) {
	for _, seed := range []app.SourceRefIdentity{
		{Source: knowl.SourceRef{Adapter: sourceRefTestGitAdapter, ID: sourceRefTestPageID}, Revision: "v1"},
		{Source: knowl.SourceRef{Adapter: sourceRefTestGitAdapter, ID: "engineering/a@b.md"}, Revision: "v1@v2"},
		{Source: knowl.SourceRef{Adapter: "git:mirror", ID: "engineering/100%@Привет.md"}, Revision: "ветка:1"},
	} {
		f.Add(seed.Source.Adapter, seed.Source.ID, seed.Revision)
	}
	f.Fuzz(func(t *testing.T, adapter, id, revision string) {
		identity := app.SourceRefIdentity{Source: knowl.SourceRef{Adapter: adapter, ID: id}, Revision: revision}
		encoded, err := app.EncodeSourceRef(identity)
		if err != nil {
			return
		}
		parsed, err := app.ParseSourceRefCandidates(encoded)
		if err != nil || !reflect.DeepEqual(parsed, []app.SourceRefIdentity{identity}) {
			t.Fatalf("round trip %q = %#v, %v; want %#v", encoded, parsed, err, identity)
		}
	})
}
