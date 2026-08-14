package app_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	decisionPageID   knowl.PageID = "decisions/badger"
	decisionPagePath string       = "wiki/decisions/badger.md"
)

func TestSourceAwareContextUpdatesExistingPageWithoutDuplicate(t *testing.T) {
	ctx := context.Background()
	workspace, store, _, unusedMaintainer := newWorkflow(t, false, nil)
	_ = unusedMaintainer

	decisionBefore := []byte("---\nid: decisions/badger\ntitle: Badger Session Decision\ntype: decision\nsource_refs:\n  - fixture:legacy@1\n---\n# Badger Session Decision\n\nBadger stores durable session state.\n")
	unrelated := []byte("---\nid: releases/current\ntitle: Current Release\ntype: note\nsource_refs:\n  - fixture:release@1\n---\n# Current Release\n\nUnrelated packaging status.\n")
	writeFixturePage(t, workspace.Root(), decisionPagePath, decisionBefore, time.Unix(10, 0).UTC())
	writeFixturePage(t, workspace.Root(), "wiki/releases/current.md", unrelated, time.Unix(20, 0).UTC())
	snapshot, err := workspace.Snapshot(ctx, "local")
	if err != nil {
		t.Fatalf("Snapshot(): %v", err)
	}
	if err := store.Rebuild(ctx, snapshot); err != nil {
		t.Fatalf("Rebuild(): %v", err)
	}

	maintainer := &updateExistingMaintainer{}
	service, err := app.NewIngestService(workspace, store, store, maintainer, app.IngestOptions{AutoApply: true})
	if err != nil {
		t.Fatalf("NewIngestService(): %v", err)
	}
	source := sourceEnvelope([]byte("# Badger Session Decision\n\nThe accepted decision now requires crash-safe recovery."))
	result, err := service.Ingest(ctx, source)
	if err != nil {
		t.Fatalf("Ingest(): %v", err)
	}
	if result.Operation.Status != knowl.StatusCommitted {
		t.Fatalf("operation status = %q, want committed", result.Operation.Status)
	}
	if len(maintainer.pages) == 0 || maintainer.pages[0].ID != decisionPageID {
		t.Fatalf("maintainer pages = %#v, want old relevant page first", maintainer.pages)
	}

	after, err := workspace.Snapshot(ctx, "local")
	if err != nil {
		t.Fatalf("Snapshot() after ingest: %v", err)
	}
	if len(after.Pages) != 2 {
		t.Fatalf("ordinary page count = %d, want 2 without duplicate", len(after.Pages))
	}
	decisionCount := 0
	for _, page := range after.Pages {
		if page.ID == decisionPageID {
			decisionCount++
			if !contains(page.Content, "crash-safe recovery") {
				t.Fatalf("decision page was not updated: %q", page.Content)
			}
		}
	}
	if decisionCount != 1 {
		t.Fatalf("decision page count = %d, want exactly one", decisionCount)
	}
}

type updateExistingMaintainer struct {
	pages []knowl.PageSnapshot
}

func (maintainer *updateExistingMaintainer) Plan(_ context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	maintainer.pages = append([]knowl.PageSnapshot(nil), input.Pages...)
	if len(input.Pages) == 0 || input.Pages[0].ID != decisionPageID {
		return knowl.ModelEditPlan{}, errors.New("expected relevant decision page first")
	}
	sourceRef := app.SourceRefKey(input.Source)
	content := []byte(fmt.Sprintf("---\nid: decisions/badger\ntitle: Badger Session Decision\ntype: decision\nsource_refs:\n  - %s\n---\n# Badger Session Decision\n\nBadger session memory now uses crash-safe recovery.\n", sourceRef))
	return knowl.ModelEditPlan{
		SchemaDigest: input.Schema.Digest,
		SourceRefs:   []string{sourceRef},
		Edits: []knowl.FileEdit{{
			Path: decisionPagePath, ExpectedDigest: input.Pages[0].Digest, Content: content,
		}},
		Rationale: "update the existing relevant decision",
	}, nil
}

func writeFixturePage(t *testing.T, root, path string, content []byte, updatedAt time.Time) {
	t.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
	if err := os.WriteFile(absolute, content, 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	if err := os.Chtimes(absolute, updatedAt, updatedAt); err != nil {
		t.Fatalf("Chtimes(%q): %v", path, err)
	}
}
