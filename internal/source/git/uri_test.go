package git_test

import (
	"net/url"
	"testing"

	"github.com/baldaworks/knowl/internal/source/git"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestGitDocumentURIProvenance(t *testing.T) {
	t.Parallel()
	for _, relative := range []string{"docs/manual.md", "docs/ADR%2D001.md", "Решения/ADR%2D003-проект.md", "docs/design notes.md", "docs/100% complete.md"} {
		t.Run(relative, func(t *testing.T) {
			for _, base := range []string{"", "https://wiki.example.com/project"} {
				uri := git.DocumentURI("git-source", base, "snapshot", relative)
				document := knowl.SourceDocument{SourceID: "git-source", DocumentID: knowl.DocumentID(relative), Revision: "revision", URI: uri}
				if err := app.ValidateSourceDocument(document); err != nil {
					t.Fatalf("base %q: provenance URI %q: %v", base, uri, err)
				}
				parsed, err := url.Parse(uri)
				if err != nil {
					t.Fatal(err)
				}
				if parsed.Scheme == "" || parsed.Host == "" {
					t.Fatalf("incomplete URI: %#v", parsed)
				}
			}
		})
	}
	// Preserve canonical fallback URIs already recorded in immutable manifests.
	const legacy = "knowl://sources/git-source/docs/ADR%2D001.md"
	if got := git.DocumentURI("git-source", "", "snapshot", "docs/ADR%2D001.md"); got != legacy {
		t.Fatalf("legacy URI = %q, want %q", got, legacy)
	}
}
