package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

const legacyMigrationLog = "# Knowl Update Log\n- {\"operation_id\":\"legacy-operation\"}\n"

func TestWorkspaceMigrateOKFV02PreservesLegacyContentAndIsIdempotent(t *testing.T) {
	workspace := legacyMigrationWorkspace(t)
	beforeBody := "# Legacy page\n\nExact body and provenance.\n"

	result, err := workspace.MigrateOKFV02(context.Background())
	if err != nil {
		t.Fatalf("MigrateOKFV02() error = %v", err)
	}
	if !result.Changed || result.Version != okf.Version || !slices.Contains(result.Files, "wiki/index.md") || !slices.Contains(result.Files, "wiki/entities/legacy.md") || !slices.Contains(result.Files, migrationLegacyLogPath) {
		t.Fatalf("MigrateOKFV02() = %#v", result)
	}
	if err := workspace.Validate(); err != nil {
		t.Fatalf("Validate() migrated workspace: %v", err)
	}
	archived, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(migrationLegacyLogPath)))
	if err != nil || string(archived) != legacyMigrationLog {
		t.Fatalf("legacy log archive = %q, %v", archived, err)
	}
	pagePath := filepath.Join(workspace.Root(), "wiki", "entities", "legacy.md")
	page, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatal(err)
	}
	document, err := okf.ParseConcept("entities/legacy.md", page, okf.DefaultLimits())
	if err != nil {
		t.Fatalf("ParseConcept() migrated page: %v", err)
	}
	frontmatter, err := knowlwiki.FrontmatterFromMetadata(document.Metadata)
	if err != nil || frontmatter.Legacy || frontmatter.ID != "entities/legacy" || len(frontmatter.SourceRefs) != 1 || frontmatter.SourceDocument == nil || frontmatter.SourceDocument.SourceID != testSourceID || document.Body != beforeBody {
		t.Fatalf("migrated page = %#v body=%q, %v", frontmatter, document.Body, err)
	}
	if _, exists := document.Metadata.Extensions["id"]; exists {
		t.Fatal("legacy flat ID survived migration")
	}
	snapshot, err := workspace.Snapshot(context.Background(), "local")
	if err != nil || len(snapshot.Pages) != 1 || snapshot.Pages[0].Body != beforeBody {
		t.Fatalf("Snapshot() = %#v, %v", snapshot, err)
	}
	pageAfterFirst := append([]byte(nil), page...)
	second, err := workspace.MigrateOKFV02(context.Background())
	if err != nil || second.Changed || len(second.Files) != 0 {
		t.Fatalf("second MigrateOKFV02() = %#v, %v", second, err)
	}
	page, err = os.ReadFile(pagePath)
	if err != nil || !slices.Equal(page, pageAfterFirst) {
		t.Fatalf("idempotent page = %q, %v", page, err)
	}
}

func TestMigrateOKFDocumentAcceptsOnlyExactLegacyStarterIndex(t *testing.T) {
	limits := okf.DefaultLimits()
	migrated, err := migrateOKFDocument(okfIndexFilename, []byte(legacyStarterIndex), limits)
	if err != nil {
		t.Fatalf("migrate exact legacy starter index: %v", err)
	}
	index, err := okf.ParseRootIndex(migrated, limits)
	if err != nil || index.ObservedVersion != okf.Version || index.Body != migratedStarterIndex {
		t.Fatalf("migrated starter index = %#v, %v, content=%q", index, err, migrated)
	}

	lookalike := []byte("# Knowl index\n\nNo pages have been committed yet!\n")
	if _, err := migrateOKFDocument(okfIndexFilename, lookalike, limits); !errors.Is(err, okf.ErrInvalid) {
		t.Fatalf("migrate lookalike error = %v, want OKF invalid", err)
	}
}

func TestWorkspaceMigrateOKFV02PreflightFailureLeavesWorkspaceUntouched(t *testing.T) {
	workspace := legacyMigrationWorkspace(t)
	pagePath := filepath.Join(workspace.Root(), "wiki", "entities", "legacy.md")
	pageBefore, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(workspace.Root(), filepath.FromSlash(migrationLegacyLogPath))
	if err := writeAtomic(archivePath, []byte("collision"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.MigrateOKFV02(context.Background()); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("MigrateOKFV02() error = %v, want precondition", err)
	}
	pageAfter, err := os.ReadFile(pagePath)
	if err != nil || !slices.Equal(pageAfter, pageBefore) {
		t.Fatalf("page changed after failed preflight: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), filepath.FromSlash(migrationMarkerPath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completion marker after failed preflight = %v", err)
	}
}

func TestWorkspaceMigrateOKFV02RecoversInjectedInterruptions(t *testing.T) {
	tests := []struct {
		name  string
		point string
		index int
	}{
		{name: "prepared", point: recoveryPrepared, index: -1},
		{name: "partially applied", point: commitFaultApplied, index: 1},
		{name: "durably committed", point: recoveryCommitted, index: -1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := legacyMigrationWorkspace(t)
			workspace.commitFault = func(point string, index int) error {
				if point == test.point && index == test.index {
					return errors.New("injected interruption")
				}
				return nil
			}
			if _, err := workspace.MigrateOKFV02(context.Background()); err == nil {
				t.Fatal("MigrateOKFV02() error = nil")
			}
			if _, err := os.Stat(filepath.Join(workspace.Root(), filepath.FromSlash(migrationMarkerPath))); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("completion marker before recovered commit = %v", err)
			}
			workspace.commitFault = nil
			if _, err := workspace.MigrateOKFV02(context.Background()); err != nil {
				t.Fatalf("recovered MigrateOKFV02() error = %v", err)
			}
			if err := workspace.Validate(); err != nil {
				t.Fatalf("Validate() recovered workspace: %v", err)
			}
		})
	}
}

func legacyMigrationWorkspace(t *testing.T) *Workspace {
	t.Helper()
	root := t.TempDir()
	for _, relative := range []string{"raw", "wiki/entities", ".knowl/staging", ".knowl/recovery", ".knowl/commits"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(relative)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"schema.md":               "# Schema\n",
		"wiki/index.md":           legacyStarterIndex,
		"wiki/log.md":             legacyMigrationLog,
		"wiki/entities/legacy.md": "---\nid: entities/legacy\ntitle: Legacy\ntype: entity\nsource_refs:\n  - raw:legacy@1\nsource_document:\n  source_id: engineering\n  document_id: legacy.md\n  revision: revision-1\n  uri: file:///legacy.md\ncustom: retained\n---\n# Legacy page\n\nExact body and provenance.\n",
	}
	for relative, content := range files {
		if err := writeAtomic(filepath.Join(root, filepath.FromSlash(relative)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, time.August, 26, 10, 0, 0, 0, time.UTC)
	workspace, err := New(root, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	return workspace
}
