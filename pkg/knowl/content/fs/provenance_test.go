package fs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	provenanceRetiringSourceID knowl.SourceID = "retiring"
	provenanceActiveSourceID   knowl.SourceID = "active"
)

const (
	provenancePageA = "concepts/a"
	provenancePageB = "concepts/b"
	provenancePageC = "concepts/c"
	provenancePageD = "concepts/d"
	provenancePathA = "wiki/concepts/a.md"
)

func TestPageProvenanceReturnsStableBoundedKeysetWindows(t *testing.T) {
	workspace := newProvenanceWorkspace(t)
	targetOwner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: provenanceRetiringSourceID}
	targetRef := acceptProvenanceSource(t, workspace, targetOwner, "target", "v1")
	otherRef := acceptProvenanceSource(t, workspace, knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: provenanceActiveSourceID}, "other", "v1")
	for _, fixture := range []struct {
		id  string
		ref string
	}{
		{provenancePageD, targetRef},
		{provenancePageB, otherRef},
		{provenancePageA, targetRef},
		{provenancePageC, targetRef},
	} {
		writeCanonicalFixture(t, workspace, "wiki/"+fixture.id+".md", validWorkspacePage(fixture.id, fixture.id, fixture.ref, "Body"))
	}
	limits := app.CanonicalProvenanceLimits{Returned: 2, Examined: 3, PathBytes: 2_048, PageBytes: 1 << 20, TotalBytes: 4 << 20, SourceRefs: 8, RoundPages: 100}

	first, err := workspace.PageProvenance(t.Context(), testScope, targetOwner, "", limits)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	assertProvenancePage(t, first, []string{provenancePathA, "wiki/concepts/c.md"}, 3, 2, 3, false)
	if first.NextCursor == "" {
		t.Fatal("first page omitted continuation cursor")
	}

	second, err := workspace.PageProvenance(t.Context(), testScope, targetOwner, first.NextCursor, limits)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	assertProvenancePage(t, second, []string{"wiki/concepts/d.md"}, 1, 1, 4, true)
	if !second.Authoritative || first.Authoritative {
		t.Fatalf("authority flags first=%t second=%t", first.Authoritative, second.Authoritative)
	}
	if second.NextCursor != "" {
		t.Fatalf("EOF cursor = %q", second.NextCursor)
	}

	restarted, err := workspace.PageProvenance(t.Context(), testScope, targetOwner, "", limits)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if !reflect.DeepEqual(first, restarted) {
		t.Fatalf("restart page differs:\nfirst=%#v\nagain=%#v", first, restarted)
	}
	for _, item := range append(first.Items, second.Items...) {
		if item.ID == "" || item.Digest == "" || len(item.SourceRefs) != 1 || item.SourceRefs[0] != targetRef {
			t.Errorf("incomplete exact provenance: %#v", item)
		}
	}
}

func TestPageProvenanceFailsClosedWithTypedOutcomes(t *testing.T) {
	targetOwner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: provenanceRetiringSourceID}
	baseLimits := app.CanonicalProvenanceLimits{Returned: 4, Examined: 8, PathBytes: 2_048, PageBytes: 1 << 20, TotalBytes: 4 << 20, SourceRefs: 8, RoundPages: 100}

	t.Run("malformed page", func(t *testing.T) {
		workspace := newProvenanceWorkspace(t)
		writeCanonicalFixture(t, workspace, "wiki/concepts/bad.md", []byte("---\nid: [\n---\n# Bad\n"))
		_, err := workspace.PageProvenance(t.Context(), testScope, targetOwner, "", baseLimits)
		assertProvenanceFailure(t, err, app.CanonicalProvenanceCodePageMalformed, app.ErrCanonicalProvenanceMalformed)
	})

	t.Run("canonical identity mismatch", func(t *testing.T) {
		workspace := newProvenanceWorkspace(t)
		ref := acceptProvenanceSource(t, workspace, targetOwner, "identity", "v1")
		writeCanonicalFixture(t, workspace, "wiki/concepts/bad.md", validWorkspacePage("concepts/other", "Bad", ref, "Body"))
		_, err := workspace.PageProvenance(t.Context(), testScope, targetOwner, "", baseLimits)
		assertProvenanceFailure(t, err, app.CanonicalProvenanceCodePageMalformed, app.ErrCanonicalProvenanceMalformed)
	})

	t.Run("malformed ref", func(t *testing.T) {
		workspace := newProvenanceWorkspace(t)
		writeCanonicalFixture(t, workspace, "wiki/concepts/bad.md", validWorkspacePage("concepts/bad", "Bad", "broken", "Body"))
		_, err := workspace.PageProvenance(t.Context(), testScope, targetOwner, "", baseLimits)
		assertProvenanceFailure(t, err, app.CanonicalProvenanceCodeRefMalformed, app.ErrCanonicalProvenanceMalformed)
	})

	t.Run("unowned ref", func(t *testing.T) {
		workspace := newProvenanceWorkspace(t)
		writeCanonicalFixture(t, workspace, "wiki/concepts/bad.md", validWorkspacePage("concepts/bad", "Bad", "wiki-filesystem:missing/doc@v1", "Body"))
		_, err := workspace.PageProvenance(t.Context(), testScope, targetOwner, "", baseLimits)
		assertProvenanceFailure(t, err, app.CanonicalProvenanceCodeRefUnowned, app.ErrSourceLifecycleUnowned)
	})

	t.Run("source refs", func(t *testing.T) {
		workspace := newProvenanceWorkspace(t)
		first := acceptProvenanceSource(t, workspace, targetOwner, "one", "v1")
		second := acceptProvenanceSource(t, workspace, targetOwner, "two", "v1")
		writeCanonicalFixture(t, workspace, "wiki/concepts/many.md", validWorkspacePageRefs("concepts/many", "Many", []string{first, second}, "Body"))
		limits := baseLimits
		limits.SourceRefs = 1
		_, err := workspace.PageProvenance(t.Context(), testScope, targetOwner, "", limits)
		assertProvenanceFailure(t, err, app.CanonicalProvenanceCodeLimit, app.ErrCanonicalProvenanceLimit)
	})

	t.Run("page bytes", func(t *testing.T) {
		workspace := newProvenanceWorkspace(t)
		ref := acceptProvenanceSource(t, workspace, targetOwner, "large", "v1")
		writeCanonicalFixture(t, workspace, "wiki/concepts/large.md", validWorkspacePage("concepts/large", "Large", ref, strings.Repeat("x", 512)))
		limits := baseLimits
		limits.PageBytes = 128
		_, err := workspace.PageProvenance(t.Context(), testScope, targetOwner, "", limits)
		assertProvenanceFailure(t, err, app.CanonicalProvenanceCodeLimit, app.ErrCanonicalProvenanceLimit)
	})

	t.Run("round pages", func(t *testing.T) {
		workspace := newProvenanceWorkspace(t)
		ref := acceptProvenanceSource(t, workspace, targetOwner, "round", "v1")
		for _, id := range []string{provenancePageA, provenancePageB, provenancePageC} {
			writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, ref, "Body"))
		}
		limits := baseLimits
		limits.RoundPages = 2
		_, err := workspace.PageProvenance(t.Context(), testScope, targetOwner, "", limits)
		assertProvenanceFailure(t, err, app.CanonicalProvenanceCodeLimit, app.ErrCanonicalProvenanceLimit)
	})

	t.Run("tampered cursor", func(t *testing.T) {
		workspace := newProvenanceWorkspace(t)
		ref := acceptProvenanceSource(t, workspace, targetOwner, "cursor", "v1")
		for _, id := range []string{provenancePageA, provenancePageB} {
			writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, ref, "Body"))
		}
		limits := baseLimits
		limits.Returned = 1
		first, err := workspace.PageProvenance(t.Context(), testScope, targetOwner, "", limits)
		if err != nil || first.NextCursor == "" {
			t.Fatalf("seed cursor: %#v, %v", first, err)
		}
		tampered := app.CanonicalProvenanceCursor(string(first.NextCursor) + "x")
		_, err = workspace.PageProvenance(t.Context(), testScope, targetOwner, tampered, limits)
		assertProvenanceFailure(t, err, app.CanonicalProvenanceCodeCursor, app.ErrCanonicalProvenanceCursor)
	})

	t.Run("noncanonical cursor encoding", func(t *testing.T) {
		workspace := newProvenanceWorkspace(t)
		ref := acceptProvenanceSource(t, workspace, targetOwner, "cursor-canonical", "v1")
		for _, id := range []string{provenancePageA, provenancePageB} {
			writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, ref, "Body"))
		}
		limits := baseLimits
		limits.Returned = 1
		first, err := workspace.PageProvenance(t.Context(), testScope, targetOwner, "", limits)
		if err != nil || first.NextCursor == "" {
			t.Fatalf("seed cursor: %#v, %v", first, err)
		}
		encoded, _, ok := strings.Cut(string(first.NextCursor), ".")
		if !ok {
			t.Fatal("seed cursor has no checksum")
		}
		payload, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		var keyed map[string]any
		if err := json.Unmarshal(payload, &keyed); err != nil {
			t.Fatal(err)
		}
		keyOrdered, err := json.Marshal(keyed)
		if err != nil {
			t.Fatal(err)
		}
		paddedPayload := slices.Clone(payload)
		for len(paddedPayload)%3 == 0 {
			paddedPayload = append(paddedPayload, ' ')
		}
		variants := []struct {
			name   string
			cursor app.CanonicalProvenanceCursor
		}{
			{"json whitespace", recomputeProvenanceCursor(append([]byte{' '}, payload...), base64.RawURLEncoding)},
			{"json key order", recomputeProvenanceCursor(keyOrdered, base64.RawURLEncoding)},
			{"base64 padding", recomputeProvenanceCursor(paddedPayload, base64.URLEncoding)},
		}
		for _, variant := range variants {
			t.Run(variant.name, func(t *testing.T) {
				_, callErr := workspace.PageProvenance(t.Context(), testScope, targetOwner, variant.cursor, limits)
				assertProvenanceFailure(t, callErr, app.CanonicalProvenanceCodeCursor, app.ErrCanonicalProvenanceCursor)
			})
		}
	})

	t.Run("cursor bound to exact owner", func(t *testing.T) {
		workspace := newProvenanceWorkspace(t)
		ref := acceptProvenanceSource(t, workspace, targetOwner, "cursor-owner", "v1")
		for _, id := range []string{provenancePageA, provenancePageB} {
			writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, ref, "Body"))
		}
		limits := baseLimits
		limits.Returned = 1
		first, err := workspace.PageProvenance(t.Context(), testScope, targetOwner, "", limits)
		if err != nil || first.NextCursor == "" {
			t.Fatalf("seed cursor: %#v, %v", first, err)
		}
		other := knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: targetOwner.SourceID}
		_, err = workspace.PageProvenance(t.Context(), testScope, other, first.NextCursor, limits)
		assertProvenanceFailure(t, err, app.CanonicalProvenanceCodeCursor, app.ErrCanonicalProvenanceCursor)
	})

	t.Run("cancellation", func(t *testing.T) {
		workspace := newProvenanceWorkspace(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := workspace.PageProvenance(ctx, testScope, targetOwner, "", baseLimits); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled error = %v", err)
		}
	})

	t.Run("unsafe path", func(t *testing.T) {
		workspace := newProvenanceWorkspace(t)
		target := filepath.Join(workspace.Root(), "wiki", "concepts", "escape.md")
		if err := os.Symlink(filepath.Join(workspace.Root(), "schema.md"), target); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		_, err := workspace.PageProvenance(t.Context(), testScope, targetOwner, "", baseLimits)
		assertProvenanceFailure(t, err, app.CanonicalProvenanceCodePageMalformed, app.ErrCanonicalProvenanceMalformed)
	})
}

func TestPageProvenanceDetectsBestEffortOutOfBandMutation(t *testing.T) {
	workspace := newProvenanceWorkspace(t)
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: provenanceRetiringSourceID}
	ref := acceptProvenanceSource(t, workspace, owner, "mutation", "v1")
	for _, id := range []string{provenancePageA, provenancePageB, provenancePageC, provenancePageD} {
		writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, ref, "Body"))
	}
	limits := app.CanonicalProvenanceLimits{Returned: 1, Examined: 4, PathBytes: 2_048, PageBytes: 1 << 20, TotalBytes: 4 << 20, SourceRefs: 8, RoundPages: 100}
	first, err := workspace.PageProvenance(t.Context(), testScope, owner, "", limits)
	if err != nil {
		t.Fatal(err)
	}
	assertProvenancePage(t, first, []string{provenancePathA}, 1, 1, 1, false)

	if err := os.Remove(filepath.Join(workspace.Root(), "wiki", "concepts", "b.md")); err != nil {
		t.Fatal(err)
	}
	writeCanonicalFixture(t, workspace, "wiki/concepts/c.md", validWorkspacePage(provenancePageC, "Changed", ref, "Changed body"))
	if err := os.Remove(filepath.Join(workspace.Root(), "wiki", "concepts", "d.md")); err != nil {
		t.Fatal(err)
	}
	writeCanonicalFixture(t, workspace, "wiki/concepts/e.md", validWorkspacePage("concepts/e", "Moved", ref, "Body"))
	writeCanonicalFixture(t, workspace, "wiki/concepts/00.md", validWorkspacePage("concepts/00", "Before", ref, "Body"))

	_, err = workspace.PageProvenance(t.Context(), testScope, owner, first.NextCursor, limits)
	assertProvenanceFailure(t, err, app.CanonicalProvenanceCodeRestart, app.ErrCanonicalProvenanceRestart)

	restarted, err := workspace.PageProvenance(t.Context(), testScope, owner, "", limits)
	if err != nil {
		t.Fatal(err)
	}
	assertProvenancePage(t, restarted, []string{"wiki/concepts/00.md"}, 1, 1, 1, false)
}

func TestPageProvenanceDetectsBestEffortOutOfBandMutationKinds(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*testing.T, *Workspace, string)
	}{
		{"insert behind cursor", func(t *testing.T, workspace *Workspace, ref string) {
			writeCanonicalFixture(t, workspace, "wiki/concepts/00.md", validWorkspacePage("concepts/00", "Before", ref, "Body"))
		}},
		{"insert ahead of cursor", func(t *testing.T, workspace *Workspace, ref string) {
			writeCanonicalFixture(t, workspace, "wiki/concepts/bb.md", validWorkspacePage("concepts/bb", "Ahead", ref, "Body"))
		}},
		{"change ahead of cursor", func(t *testing.T, workspace *Workspace, ref string) {
			writeCanonicalFixture(t, workspace, "wiki/concepts/b.md", validWorkspacePage(provenancePageB, "Changed", ref, "Changed body"))
		}},
		{"delete ahead of cursor", func(t *testing.T, workspace *Workspace, _ string) {
			if err := os.Remove(filepath.Join(workspace.Root(), "wiki", "concepts", "b.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"move ahead of cursor", func(t *testing.T, workspace *Workspace, _ string) {
			if err := os.Rename(filepath.Join(workspace.Root(), "wiki", "concepts", "d.md"), filepath.Join(workspace.Root(), "wiki", "concepts", "e.md")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			workspace := newProvenanceWorkspace(t)
			owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: provenanceRetiringSourceID}
			ref := acceptProvenanceSource(t, workspace, owner, knowl.DocumentID("mutation-"+strings.ReplaceAll(mutation.name, " ", "-")), "v1")
			for _, id := range []string{provenancePageA, provenancePageB, provenancePageC, provenancePageD} {
				writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, ref, "Body"))
			}
			limits := app.CanonicalProvenanceLimits{Returned: 1, Examined: 4, PathBytes: 2_048, PageBytes: 1 << 20, TotalBytes: 4 << 20, SourceRefs: 8, RoundPages: 100}
			first, err := workspace.PageProvenance(t.Context(), testScope, owner, "", limits)
			if err != nil {
				t.Fatal(err)
			}
			mutation.mutate(t, workspace, ref)
			_, err = workspace.PageProvenance(t.Context(), testScope, owner, first.NextCursor, limits)
			assertProvenanceFailure(t, err, app.CanonicalProvenanceCodeRestart, app.ErrCanonicalProvenanceRestart)
			restarted, err := workspace.PageProvenance(t.Context(), testScope, owner, "", limits)
			if err != nil || restarted.Items[0].Path == "" {
				t.Fatalf("restart = %#v, %v", restarted, err)
			}
		})
	}
}

func TestPageProvenanceBestEffortFenceRejectsFalseZeroAfterOutOfBandBehindInsert(t *testing.T) {
	workspace := newProvenanceWorkspace(t)
	target := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: provenanceRetiringSourceID}
	targetRef := acceptProvenanceSource(t, workspace, target, "target", "v1")
	other := knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: provenanceActiveSourceID}
	otherRef := acceptProvenanceSource(t, workspace, other, "other", "v1")
	for index := range 1_025 {
		id := fmt.Sprintf("concepts/page-%04d", index)
		writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, otherRef, "Body"))
	}
	limits := app.CanonicalProvenanceLimits{Returned: 32, Examined: 1_024, PathBytes: 2_048, PageBytes: 1 << 20, TotalBytes: 4 << 20, SourceRefs: 8, RoundPages: 2_000}
	first, err := workspace.PageProvenance(t.Context(), testScope, target, "", limits)
	if err != nil || first.EOF || first.Returned != 0 || first.Examined != 1_024 || first.Authoritative {
		t.Fatalf("first window = %#v, %v", first, err)
	}
	stable, err := workspace.PageProvenance(t.Context(), testScope, target, first.NextCursor, limits)
	if err != nil || !stable.EOF || !stable.Authoritative || stable.Examined != 1 || stable.RoundPages != 1_025 {
		t.Fatalf("stable 1024 continuation = %#v, %v", stable, err)
	}
	writeCanonicalFixture(t, workspace, "wiki/concepts/page-0000a.md", validWorkspacePage("concepts/page-0000a", "Inserted", targetRef, "Body"))

	_, err = workspace.PageProvenance(t.Context(), testScope, target, first.NextCursor, limits)
	assertProvenanceFailure(t, err, app.CanonicalProvenanceCodeRestart, app.ErrCanonicalProvenanceRestart)
	restarted, err := workspace.PageProvenance(t.Context(), testScope, target, "", limits)
	if err != nil || restarted.Returned != 1 || restarted.Items[0].Path != "wiki/concepts/page-0000a.md" {
		t.Fatalf("restart = %#v, %v", restarted, err)
	}
}

func TestPageProvenanceRejectsCanonicalCursorWithForgedProgress(t *testing.T) {
	workspace := newProvenanceWorkspace(t)
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: provenanceRetiringSourceID}
	ref := acceptProvenanceSource(t, workspace, owner, "cursor-progress", "v1")
	for _, id := range []string{provenancePageA, provenancePageB, provenancePageC} {
		writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, ref, "Body"))
	}
	limits := app.CanonicalProvenanceLimits{Returned: 1, Examined: 4, PathBytes: 2_048, PageBytes: 1 << 20, TotalBytes: 4 << 20, SourceRefs: 8, RoundPages: 100_000}
	first, err := workspace.PageProvenance(t.Context(), testScope, owner, "", limits)
	if err != nil || first.NextCursor == "" || first.RoundPages != 1 {
		t.Fatalf("seed cursor = %#v, %v", first, err)
	}
	state, err := decodeCanonicalProvenanceCursor(first.NextCursor, testScope, owner, limits)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*canonicalProvenanceCursorState)
	}{
		{"wrong ordinal", func(state *canonicalProvenanceCursorState) { state.RoundPages = 2 }},
		{"near ceiling ordinal", func(state *canonicalProvenanceCursorState) { state.RoundPages = 99_999 }},
		{"nonexistent after", func(state *canonicalProvenanceCursorState) { state.After = "wiki/concepts/aa.md" }},
		{"existing after wrong ordinal", func(state *canonicalProvenanceCursorState) { state.After = "wiki/concepts/b.md" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			forged := state
			test.mutate(&forged)
			cursor, err := encodeCanonicalProvenanceCursor(forged)
			if err != nil {
				t.Fatal(err)
			}
			_, err = workspace.PageProvenance(t.Context(), testScope, owner, cursor, limits)
			assertProvenanceFailure(t, err, app.CanonicalProvenanceCodeCursor, app.ErrCanonicalProvenanceCursor)
		})
	}
}

func TestPageProvenanceRestartsPriorWorkspaceInstanceCursor(t *testing.T) {
	workspace := newProvenanceWorkspace(t)
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: provenanceRetiringSourceID}
	ref := acceptProvenanceSource(t, workspace, owner, "process-restart", "v1")
	for _, id := range []string{provenancePageA, provenancePageB} {
		writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, ref, "Body"))
	}
	limits := app.CanonicalProvenanceLimits{Returned: 1, Examined: 4, PathBytes: 2_048, PageBytes: 1 << 20, TotalBytes: 4 << 20, SourceRefs: 8, RoundPages: 100}
	first, err := workspace.PageProvenance(t.Context(), testScope, owner, "", limits)
	if err != nil || first.NextCursor == "" {
		t.Fatalf("seed cursor = %#v, %v", first, err)
	}

	reopened, err := New(workspace.Root())
	if err != nil {
		t.Fatal(err)
	}
	_, err = reopened.PageProvenance(t.Context(), testScope, owner, first.NextCursor, limits)
	assertProvenanceFailure(t, err, app.CanonicalProvenanceCodeRestart, app.ErrCanonicalProvenanceRestart)
	restarted, err := reopened.PageProvenance(t.Context(), testScope, owner, "", limits)
	if err != nil || restarted.Items[0].Path != provenancePathA {
		t.Fatalf("empty-cursor restart = %#v, %v", restarted, err)
	}
}

func TestWorkspacePublicationGenerationCoversSupportedWriters(t *testing.T) {
	t.Run("initialization", func(t *testing.T) {
		workspace, err := New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		before := workspace.publicationGeneration
		if err := workspace.Init(); err != nil {
			t.Fatal(err)
		}
		assertPublicationAdvanced(t, workspace, before)
	})

	t.Run("accepted provenance create and enrichment", func(t *testing.T) {
		workspace := newProvenanceWorkspace(t)
		content := []byte("accepted provenance")
		digest := sha256.Sum256(content)
		envelope := knowl.SourceEnvelope{
			Scope: testScope, Source: knowl.SourceRef{Adapter: string(knowl.SourceAdapterFilesystem), ID: "retiring/generation-document"},
			Version: knowl.SourceVersion{Version: "v1", Digest: hex.EncodeToString(digest[:])}, MediaType: testMarkdownMediaType, Content: content,
		}
		before := workspace.publicationGeneration
		if _, err := workspace.AcceptSource(t.Context(), envelope); err != nil {
			t.Fatal(err)
		}
		assertPublicationAdvanced(t, workspace, before)
		before = workspace.publicationGeneration
		envelope.SourceDocument = knowl.SourceDocument{
			SourceID: provenanceRetiringSourceID, DocumentID: "generation-document", Revision: "v1", URI: "https://generation.example.test/source",
		}
		if _, err := workspace.AcceptSource(t.Context(), envelope); err != nil {
			t.Fatal(err)
		}
		assertPublicationAdvanced(t, workspace, before)
	})

	t.Run("common canonical commit", func(t *testing.T) {
		workspace := newProvenanceWorkspace(t)
		owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: provenanceRetiringSourceID}
		ref := acceptProvenanceSource(t, workspace, owner, "published", "v1")
		writeRootCatalogTargets(t, workspace, "concepts/published.md")
		schema, err := workspace.Schema(t.Context(), testScope)
		if err != nil {
			t.Fatal(err)
		}
		staged, err := workspace.StagePlan(t.Context(), knowl.ValidatedEditPlan{
			OperationID: "publication-generation", Scope: testScope, SchemaDigest: schema.Digest,
			RequiredSourceRef: ref, SourceRefs: []string{ref},
			Edits: []knowl.FileEdit{{Path: "wiki/concepts/published.md", Content: validWorkspacePage("concepts/published", "Published", ref, "Body")}},
		})
		if err != nil {
			t.Fatal(err)
		}
		before := workspace.publicationGeneration
		if _, err := workspace.Commit(t.Context(), staged); err != nil {
			t.Fatal(err)
		}
		assertPublicationAdvanced(t, workspace, before)
	})

	t.Run("prepared recovery rollback", func(t *testing.T) {
		workspace := newProvenanceWorkspace(t)
		ref := acceptProvenanceSource(t, workspace, knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: provenanceRetiringSourceID}, "recovery", "v1")
		targetRelative := "wiki/concepts/recovery.md"
		original := validWorkspacePage("concepts/recovery", "Recovery", ref, "Body")
		writeCanonicalFixture(t, workspace, targetRelative, original)
		operationID := "provenance-recovery"
		recoveryDir := filepath.Join(workspace.Root(), knowlDir, "recovery", token(operationID))
		if err := os.MkdirAll(recoveryDir, 0o700); err != nil {
			t.Fatal(err)
		}
		backup := filepath.Join(recoveryDir, "page.old")
		if err := writeAtomic(backup, original, 0o600); err != nil {
			t.Fatal(err)
		}
		writeCanonicalFixture(t, workspace, targetRelative, []byte("partial"))
		journal := recoveryJournal{
			OperationID: operationID, State: recoveryPrepared,
			Entries: []recoveryEntry{{Target: targetRelative, Backup: backup, HadOld: true, Mode: 0o600}},
		}
		if err := writeJournal(filepath.Join(workspace.Root(), knowlDir, "recovery", token(operationID)+".yaml"), journal); err != nil {
			t.Fatal(err)
		}
		before := workspace.publicationGeneration
		if _, err := workspace.Recover(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertPublicationAdvanced(t, workspace, before)
	})
}

func TestPageProvenanceRestartsAfterSupportedPublicationBetweenWindows(t *testing.T) {
	tests := []struct {
		name     string
		insertID string
		wantPath string
	}{
		{"behind cursor", "concepts/00", "wiki/concepts/00.md"},
		{"ahead of cursor", provenancePageB, provenancePathA},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := newProvenanceWorkspace(t)
			owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: provenanceRetiringSourceID}
			ref := acceptProvenanceSource(t, workspace, owner, knowl.DocumentID("supported-"+strings.ReplaceAll(test.name, " ", "-")), "v1")
			for _, id := range []string{provenancePageA, provenancePageC} {
				writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, ref, "Body"))
			}
			writeRootCatalogTargets(t, workspace, "concepts/a.md", "concepts/c.md")
			limits := app.CanonicalProvenanceLimits{Returned: 1, Examined: 4, PathBytes: 2_048, PageBytes: 1 << 20, TotalBytes: 4 << 20, SourceRefs: 8, RoundPages: 100}
			first, err := workspace.PageProvenance(t.Context(), testScope, owner, "", limits)
			if err != nil || first.NextCursor == "" {
				t.Fatalf("seed cursor = %#v, %v", first, err)
			}
			staged := stageProvenanceInsert(t, workspace, "supported-"+strings.ReplaceAll(test.name, " ", "-"), test.insertID, ref)
			if _, err := workspace.Commit(t.Context(), staged); err != nil {
				t.Fatal(err)
			}
			_, err = workspace.PageProvenance(t.Context(), testScope, owner, first.NextCursor, limits)
			assertProvenanceFailure(t, err, app.CanonicalProvenanceCodeRestart, app.ErrCanonicalProvenanceRestart)
			restarted, err := workspace.PageProvenance(t.Context(), testScope, owner, "", limits)
			if err != nil || restarted.Items[0].Path != test.wantPath {
				t.Fatalf("restart = %#v, %v", restarted, err)
			}
		})
	}
}

func TestPageProvenanceRestartsAfterSupportedRecoveryRollback(t *testing.T) {
	workspace := newProvenanceWorkspace(t)
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: provenanceRetiringSourceID}
	ref := acceptProvenanceSource(t, workspace, owner, "supported-recovery", "v1")
	for _, id := range []string{provenancePageA, provenancePageC} {
		writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, ref, "Body"))
	}
	writeRootCatalogTargets(t, workspace, "concepts/a.md", "concepts/c.md")
	limits := app.CanonicalProvenanceLimits{Returned: 1, Examined: 4, PathBytes: 2_048, PageBytes: 1 << 20, TotalBytes: 4 << 20, SourceRefs: 8, RoundPages: 100}
	first, err := workspace.PageProvenance(t.Context(), testScope, owner, "", limits)
	if err != nil || first.NextCursor == "" {
		t.Fatalf("seed cursor = %#v, %v", first, err)
	}
	staged := stageProvenanceInsert(t, workspace, "supported-recovery", provenancePageB, ref)
	workspace.commitFault = func(point string, index int) error {
		if point == commitFaultApplied && index == 0 {
			return errInjectedCommitFault
		}
		return nil
	}
	if _, err := workspace.Commit(t.Context(), staged); err == nil {
		t.Fatal("faulted commit succeeded")
	}
	workspace.commitFault = nil
	if _, err := workspace.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, err = workspace.PageProvenance(t.Context(), testScope, owner, first.NextCursor, limits)
	assertProvenanceFailure(t, err, app.CanonicalProvenanceCodeRestart, app.ErrCanonicalProvenanceRestart)
	restarted, err := workspace.PageProvenance(t.Context(), testScope, owner, "", limits)
	if err != nil || restarted.Items[0].Path != provenancePathA {
		t.Fatalf("restart = %#v, %v", restarted, err)
	}
}

func TestPageProvenanceBoundsFlatDirectoryCandidateMemory(t *testing.T) {
	workspace := newProvenanceWorkspace(t)
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: provenanceRetiringSourceID}
	ref := acceptProvenanceSource(t, workspace, owner, "flat", "v1")
	for index := range 1_100 {
		id := fmt.Sprintf("concepts/flat-%04d", index)
		writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, ref, "Body"))
	}
	limits := app.CanonicalProvenanceLimits{Returned: 1, Examined: 1_024, PathBytes: 2_048, PageBytes: 1 << 20, TotalBytes: 4 << 20, SourceRefs: 8, RoundPages: 2_000}
	sources, err := workspace.operatorSources(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	wiki, err := openReadRoot(workspace.root, workspaceWikiDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wiki.close)
	inventory, err := canonicalProvenanceInventory(t.Context(), wiki, sources, "", limits)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.scanned < 1_100 || inventory.peak != limits.Examined+1 || len(inventory.candidates) != limits.Examined+1 {
		t.Fatalf("resource report: scanned=%d peak=%d candidates=%d", inventory.scanned, inventory.peak, len(inventory.candidates))
	}
}

func TestPageProvenanceConvergesAcrossDefaultWindowsAndProvesZeroRefs(t *testing.T) {
	workspace := newProvenanceWorkspace(t)
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: provenanceRetiringSourceID}
	ref := acceptProvenanceSource(t, workspace, owner, "many", "v1")
	for index := range 35 {
		id := fmt.Sprintf("concepts/page-%02d", index)
		writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, ref, "Body"))
	}

	var cursor app.CanonicalProvenanceCursor
	paths := make([]string, 0, 35)
	for calls := 0; ; calls++ {
		if calls > 2 {
			t.Fatal("pager did not converge")
		}
		page, err := workspace.PageProvenance(t.Context(), testScope, owner, cursor, app.CanonicalProvenanceLimits{})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			paths = append(paths, item.Path)
		}
		if page.EOF {
			if page.RoundPages != 35 {
				t.Fatalf("complete round examined = %d", page.RoundPages)
			}
			break
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			t.Fatalf("cursor did not advance: %q", page.NextCursor)
		}
		cursor = page.NextCursor
	}
	if len(paths) != 35 || !slices.IsSorted(paths) {
		t.Fatalf("paths = %v", paths)
	}

	empty, err := workspace.PageProvenance(t.Context(), testScope, knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "absent"}, "", app.CanonicalProvenanceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if !empty.EOF || !empty.Authoritative || empty.Returned != 0 || len(empty.Items) != 0 || empty.Examined != 35 || empty.RoundPages != 35 || empty.NextCursor != "" {
		t.Fatalf("zero-ref verification = %#v", empty)
	}
}

func TestPageProvenanceKeepsEqualSourceIDsAdapterQualified(t *testing.T) {
	workspace := newProvenanceWorkspace(t)
	filesystem := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: "shared"}
	git := knowl.SourceOwner{Adapter: knowl.SourceAdapterGit, SourceID: "shared"}
	filesystemRef := acceptProvenanceSource(t, workspace, filesystem, "old", "same")
	gitRef := acceptProvenanceSource(t, workspace, git, "new", "same")
	writeCanonicalFixture(t, workspace, "wiki/concepts/old.md", validWorkspacePage("concepts/old", "Old", filesystemRef, "Body"))
	writeCanonicalFixture(t, workspace, "wiki/concepts/new.md", validWorkspacePage("concepts/new", "New", gitRef, "Body"))

	page, err := workspace.PageProvenance(t.Context(), testScope, filesystem, "", app.CanonicalProvenanceLimits{})
	if err != nil {
		t.Fatal(err)
	}
	assertProvenancePage(t, page, []string{"wiki/concepts/old.md"}, 2, 1, 2, true)
}

func TestPageProvenanceOrdersByFullCanonicalSlashPath(t *testing.T) {
	workspace := newProvenanceWorkspace(t)
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: provenanceRetiringSourceID}
	ref := acceptProvenanceSource(t, workspace, owner, "ordering", "v1")
	for _, id := range []string{"concepts/a/child", provenancePageA, "concepts/a!/child"} {
		writeCanonicalFixture(t, workspace, "wiki/"+id+".md", validWorkspacePage(id, id, ref, "Body"))
	}
	limits := app.CanonicalProvenanceLimits{Returned: 1, Examined: 4, PathBytes: 2_048, PageBytes: 1 << 20, TotalBytes: 4 << 20, SourceRefs: 8, RoundPages: 100}

	var cursor app.CanonicalProvenanceCursor
	var paths []string
	for {
		page, err := workspace.PageProvenance(t.Context(), testScope, owner, cursor, limits)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			paths = append(paths, item.Path)
		}
		if page.EOF {
			break
		}
		cursor = page.NextCursor
	}
	want := []string{"wiki/concepts/a!/child.md", provenancePathA, "wiki/concepts/a/child.md"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

func TestPageProvenanceAppliesTotalByteCeilingAcrossExaminedPages(t *testing.T) {
	workspace := newProvenanceWorkspace(t)
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: provenanceRetiringSourceID}
	ref := acceptProvenanceSource(t, workspace, owner, "bytes", "v1")
	content := validWorkspacePage(provenancePageA, "A", ref, strings.Repeat("x", 128))
	writeCanonicalFixture(t, workspace, provenancePathA, content)
	writeCanonicalFixture(t, workspace, "wiki/concepts/b.md", validWorkspacePage(provenancePageB, "B", ref, strings.Repeat("x", 128)))
	limits := app.CanonicalProvenanceLimits{Returned: 4, Examined: 4, PathBytes: 2_048, PageBytes: len(content), TotalBytes: len(content) + 1, SourceRefs: 8, RoundPages: 100}
	_, err := workspace.PageProvenance(t.Context(), testScope, owner, "", limits)
	assertProvenanceFailure(t, err, app.CanonicalProvenanceCodeLimit, app.ErrCanonicalProvenanceLimit)
}

func newProvenanceWorkspace(t *testing.T) *Workspace {
	t.Helper()
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	return workspace
}

func acceptProvenanceSource(t *testing.T, workspace *Workspace, owner knowl.SourceOwner, documentID knowl.DocumentID, revision string) string {
	t.Helper()
	content := []byte(fmt.Sprintf("%s/%s@%s", owner.SourceID, documentID, revision))
	digest := sha256.Sum256(content)
	accepted, err := workspace.AcceptSource(context.Background(), knowl.SourceEnvelope{
		Scope:     testScope,
		Source:    knowl.SourceRef{Adapter: string(owner.Adapter), ID: string(owner.SourceID) + "/" + string(documentID)},
		Version:   knowl.SourceVersion{Version: revision, Digest: fmt.Sprintf("%x", digest)},
		MediaType: testMarkdownMediaType,
		SourceDocument: knowl.SourceDocument{
			SourceID: owner.SourceID, DocumentID: documentID, Revision: revision,
			URI: "https://example.test/" + string(owner.SourceID) + "/" + string(documentID),
		},
		Content: content,
	})
	if err != nil {
		t.Fatalf("accept provenance source: %v", err)
	}
	return sourceRefKey(accepted)
}

func assertProvenancePage(t *testing.T, page app.CanonicalProvenancePage, wantPaths []string, examined, returned, roundPages int, eof bool) {
	t.Helper()
	paths := make([]string, len(page.Items))
	for index, item := range page.Items {
		paths[index] = item.Path
	}
	if !reflect.DeepEqual(paths, wantPaths) || page.Examined != examined || page.Returned != returned || page.RoundPages != roundPages || page.EOF != eof {
		t.Fatalf("page = %#v, paths = %v", page, paths)
	}
}

func assertProvenanceFailure(t *testing.T, err error, code string, cause error) {
	t.Helper()
	var failure *app.CanonicalProvenanceFailure
	if !errors.As(err, &failure) || failure.Code != code || !errors.Is(err, cause) {
		t.Fatalf("failure = %#v (%v), want code %q and cause %v", failure, err, code, cause)
	}
}

func recomputeProvenanceCursor(payload []byte, encoding *base64.Encoding) app.CanonicalProvenanceCursor {
	digest := sha256.Sum256(payload)
	return app.CanonicalProvenanceCursor(encoding.EncodeToString(payload) + "." + hex.EncodeToString(digest[:16]))
}

func assertPublicationAdvanced(t *testing.T, workspace *Workspace, before uint64) {
	t.Helper()
	if workspace.publicationGeneration != before+1 {
		t.Fatalf("publication generation = %d, want %d", workspace.publicationGeneration, before+1)
	}
}

func stageProvenanceInsert(t *testing.T, workspace *Workspace, operationID, id, ref string) knowl.StagedChange {
	t.Helper()
	indexPath := filepath.Join(workspace.Root(), canonicalIndexPath)
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	indexAfter := append(slices.Clone(indexBefore), []byte("\n* [Inserted]("+id+".md)\n")...)
	schema, err := workspace.Schema(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := workspace.StagePlan(t.Context(), knowl.ValidatedEditPlan{
		OperationID: operationID, Scope: testScope, SchemaDigest: schema.Digest,
		RequiredSourceRef: ref, SourceRefs: []string{ref},
		Edits: []knowl.FileEdit{
			{Path: "wiki/" + id + ".md", Content: validWorkspacePage(id, "Inserted", ref, "Body")},
			{Path: canonicalIndexPath, ExpectedDigest: digestBytes(indexBefore), Content: indexAfter},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return staged
}
