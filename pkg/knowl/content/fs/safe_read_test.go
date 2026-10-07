package fs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const readTestSourceID = "source-1"

func TestCanonicalReadLimits(t *testing.T) {
	for _, limit := range []knowl.ReadLimits{{Bytes: 4}, {Characters: 4}, {}} {
		t.Run(string(rune('a'+limit.Bytes+limit.Characters)), func(t *testing.T) {
			workspace := newSourceStageWorkspace(t)
			workspace.maxSourceBytes = 32
			writeCanonicalFixture(t, workspace, "wiki/entities/bounded.md", bytes.Repeat([]byte("界"), 20))
			_, err := workspace.ReadPages(t.Context(), testScope, []knowl.PageID{"entities/bounded"}, limit)
			if !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
				t.Fatalf("ReadPages() = %v, want read limit", err)
			}
			writeCanonicalFixture(t, workspace, canonicalIndexPath, bytes.Repeat([]byte("x"), 64))
			_, err = workspace.readControlPage(t.Context(), "index", limit)
			if !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
				t.Fatalf("readControlPage() = %v, want read limit", err)
			}
		})
	}
}

func TestCanonicalReadRejectsAmbiguousIDs(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	writeCanonicalFixture(t, workspace, "wiki/entities/page.md", validWorkspacePage("entities/page", "Page", testWorkspaceSourceRef, ""))
	for _, id := range []knowl.PageID{"entities/../entities/page", "entities//page", "entities/%252e%252e/page", "entities/%2e%2e/page", "wiki/../wiki/entities/page"} {
		_, err := workspace.ReadPages(t.Context(), testScope, []knowl.PageID{id}, knowl.ReadLimits{})
		if !errors.Is(err, ErrPathRejected) {
			t.Errorf("ReadPages(%q) = %v, want path rejected", id, err)
		}
	}
}

func TestCanonicalReadPublicationGuard(t *testing.T) {
	for _, point := range []string{recoveryPrepared, commitFaultApplied, recoveryCommitted} {
		t.Run(point, func(t *testing.T) {
			workspace := newSourceStageWorkspace(t)
			target := "wiki/sources/engineering/read-guard.md"
			before := sourcePage("sources/engineering/read-guard", "page", "v1")
			after := sourcePage("sources/engineering/read-guard", "page", "v2")
			writeCanonicalFixture(t, workspace, target, before)
			staged, err := workspace.StageSourcePlan(t.Context(), sourcePlan("sync-read-guard", testSourceID, knowl.SourceMutation{Action: knowl.SourceMutationWrite, Path: target, ExpectedDigest: digestBytes(before), Content: after}))
			if err != nil {
				t.Fatal(err)
			}
			workspace.commitFault = func(at string, _ int) error {
				if at == point {
					return errInjectedCommitFault
				}
				return nil
			}
			if _, err := workspace.CommitSource(t.Context(), staged); !errors.Is(err, errInjectedCommitFault) {
				t.Fatal(err)
			}
			journalDir := filepath.Join(workspace.root, knowlDir, "recovery")
			inventory, err := os.ReadDir(journalDir)
			if err != nil {
				t.Fatal(err)
			}
			pages, err := workspace.ReadPages(t.Context(), testScope, []knowl.PageID{"sources/engineering/read-guard"}, knowl.ReadLimits{})
			if point == recoveryCommitted {
				if err != nil || len(pages) != 1 || pages[0].Content != string(after) {
					t.Fatalf("committed read = %#v, %v", pages, err)
				}
			} else if !errors.Is(err, app.ErrOperatorWorkspaceUnavailable) {
				t.Fatalf("prepared read = %#v, %v", pages, err)
			}
			_, controlErr := workspace.readControlPage(t.Context(), "index", knowl.ReadLimits{})
			if point != recoveryCommitted && !errors.Is(controlErr, app.ErrOperatorWorkspaceUnavailable) {
				t.Fatalf("prepared control read = %v", controlErr)
			}
			remaining, err := os.ReadDir(journalDir)
			if err != nil || len(remaining) != len(inventory) {
				t.Fatalf("read changed journal inventory: %v", err)
			}
			if _, err := workspace.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			pages, err = workspace.ReadPages(t.Context(), testScope, []knowl.PageID{"sources/engineering/read-guard"}, knowl.ReadLimits{})
			want := before
			if point == recoveryCommitted {
				want = after
			}
			if err != nil || len(pages) != 1 || pages[0].Content != string(want) {
				t.Fatalf("recovered read = %#v, %v", pages, err)
			}
		})
	}
}

func TestCanonicalReadSourcePreservesErrors(t *testing.T) {
	workspace := newSourceStageWorkspace(t)
	source := knowl.AcceptedSource{Scope: testScope, Source: knowl.SourceRef{Adapter: testFixtureAdapter, ID: readTestSourceID}, Version: knowl.SourceVersion{Version: "1", Digest: digestBytes([]byte("source content"))}}
	if _, err := workspace.ReadSource(t.Context(), source, knowl.ReadLimits{Bytes: 1}); !errors.Is(err, app.ErrOperatorReadLimitExceeded) || !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("source limit = %v", err)
	}
	source.Version.Digest = digestBytes([]byte("different"))
	if _, err := workspace.ReadSource(t.Context(), source, knowl.ReadLimits{}); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("source mismatch = %v", err)
	}
	if err := os.RemoveAll(filepath.Join(workspace.root, workspaceRawDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ReadSource(t.Context(), source, knowl.ReadLimits{}); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("source missing = %v", err)
	}
}

func TestCanonicalReadRejectsUnsafeJournalsWithoutMutation(t *testing.T) {
	for _, fixture := range []string{recoveryPrepared, "malformed", "oversized", "symlink", "invalid committed", "diverged committed"} {
		t.Run(fixture, func(t *testing.T) {
			workspace := newSourceStageWorkspace(t)
			target := "wiki/entities/page.md"
			page := validWorkspacePage("entities/page", "Page", testWorkspaceSourceRef, "")
			writeCanonicalFixture(t, workspace, target, page)
			journalPath := filepath.Join(workspace.root, knowlDir, "recovery", token("unsafe-read")+".yaml")
			journal := recoveryJournal{OperationID: "unsafe-read", State: recoveryPrepared, Entries: []recoveryEntry{{Target: target, Digest: digestBytes(page)}}}
			switch fixture {
			case recoveryPrepared:
				if err := writeJournal(journalPath, journal); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(journalPath, []byte("entries: ["), 0o600); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				file, err := os.Create(journalPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Truncate(maxRecoveryJournalBytes + 1); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				createReadTestSymlink(t, filepath.Join(workspace.root, target), journalPath)
			case "invalid committed":
				journal.State = recoveryCommitted
				journal.Entries[0].Target = "../outside"
				if err := writeJournal(journalPath, journal); err != nil {
					t.Fatal(err)
				}
			case "diverged committed":
				journal.State = recoveryCommitted
				journal.Entries[0].Digest = digestBytes([]byte("other publication"))
				if err := writeJournal(journalPath, journal); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := workspace.ReadPages(t.Context(), testScope, []knowl.PageID{"entities/page"}, knowl.ReadLimits{}); !errors.Is(err, app.ErrOperatorWorkspaceUnavailable) {
				t.Fatalf("unsafe read = %v", err)
			}
			source := knowl.AcceptedSource{Scope: testScope, Source: knowl.SourceRef{Adapter: testFixtureAdapter, ID: readTestSourceID}, Version: knowl.SourceVersion{Version: "1"}}
			if _, err := workspace.ReadSource(t.Context(), source, knowl.ReadLimits{}); !errors.Is(err, app.ErrOperatorWorkspaceUnavailable) {
				t.Fatalf("unsafe source read = %v", err)
			}
			after, err := os.ReadFile(journalPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("read mutated journal: %v", err)
			}
			actual, err := os.ReadFile(filepath.Join(workspace.root, target))
			if err != nil || !bytes.Equal(actual, page) {
				t.Fatalf("read mutated page: %v", err)
			}
		})
	}
}

func TestCanonicalReadRejectsUnsafeRawManifestAccess(t *testing.T) {
	for _, fixture := range []string{"large manifest", "manifest link", "source link", "raw root link"} {
		t.Run(fixture, func(t *testing.T) {
			workspace := newSourceStageWorkspace(t)
			writeCanonicalFixture(t, workspace, "wiki/entities/page.md", validWorkspacePage("entities/page", "Page", testWorkspaceSourceRef, ""))
			raw := filepath.Join(workspace.root, workspaceRawDir)
			sourceDir := filepath.Join(raw, token(testScope+"\x00"+testFixtureAdapter+"\x00"+readTestSourceID), token("1"))
			manifestPath := filepath.Join(sourceDir, "manifest.yaml")
			want := ErrPathRejected
			switch fixture {
			case "large manifest":
				want = app.ErrOperatorReadLimitExceeded
				file, err := os.Create(manifestPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Truncate(maxStageManifestBytes + 1); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			case "manifest link":
				if err := os.Rename(manifestPath, manifestPath+".saved"); err != nil {
					t.Fatal(err)
				}
				createReadTestSymlink(t, manifestPath+".saved", manifestPath)
			case "source link":
				sourcePath := filepath.Join(sourceDir, "source")
				if err := os.Remove(sourcePath); err != nil {
					t.Fatal(err)
				}
				createReadTestSymlink(t, filepath.Join(workspace.root, schemaFile), sourcePath)
			case "raw root link":
				if err := os.Rename(raw, raw+"-saved"); err != nil {
					t.Fatal(err)
				}
				createReadTestSymlink(t, raw+"-saved", raw)
			}
			if _, err := workspace.ReadPages(t.Context(), testScope, []knowl.PageID{"entities/page"}, knowl.ReadLimits{}); !errors.Is(err, want) {
				t.Fatalf("raw inspection error = %v, want %v", err, want)
			}
		})
	}
}
