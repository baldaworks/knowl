package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const migrationCatalogSourceID = "catalog"

func TestMigrateOKFV02CommandMigratesAndRebuildsProjection(t *testing.T) {
	workspaceRoot := writeLegacyCommandWorkspace(t)
	configureMigrationCommand(t, workspaceRoot)
	stdout, stderr, err := executeCLICommand(newRootCommand(), []string{migrateCommandName, migrateOKFV02Name}, nil)
	if err != nil {
		t.Fatalf("migrate command: %v, stderr=%s", err, stderr)
	}
	var result contentfs.MigrationResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil || !result.Changed {
		t.Fatalf("migration result = %#v, %v, output=%q", result, err, stdout)
	}
	workspace, err := contentfs.New(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Validate(); err != nil {
		t.Fatalf("migrated workspace: %v", err)
	}
	store, err := sqlite.Open(context.Background(), filepath.Join(workspaceRoot, ".knowl", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	references, err := store.Search(context.Background(), "local", "migrationcommandbeacon", knowl.ReadLimits{Pages: 1, Characters: 64}, nil)
	if err != nil || len(references) != 1 || references[0].OKF == nil {
		t.Fatalf("rebuilt projection = %#v, %v", references, err)
	}

	stdout, stderr, err = executeCLICommand(newRootCommand(), []string{migrateCommandName, migrateOKFV02Name}, nil)
	if err != nil || strings.TrimSpace(stderr) != "" {
		t.Fatalf("repeat migrate command: %v, stderr=%s", err, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil || result.Changed || len(result.Files) != 0 {
		t.Fatalf("repeat migration result = %#v, %v", result, err)
	}
}

func TestOKFV02EndToEndMigratesSyncsRestartsAndRetrieves(t *testing.T) {
	workspaceRoot := writeLegacyCommandWorkspace(t)
	sourceRoot := t.TempDir()
	for relative, content := range map[string]string{
		"index.md":           "---\nokf_version: \"0.2\"\n---\n# Catalog\n\n* [Metric](metrics/revenue.md)\n",
		"log.md":             "# Catalog Log\n\n## 2026-08-26\n* Published fixture\n",
		"metrics/revenue.md": "---\ntype: Metric\ntitle: Revenue\ndescription: Recognized revenue\nverified: {by: human:reviewer, at: 2026-08-26T09:00:00+06:00}\nproducer: fixture\n---\n# Revenue\n\nOkfendtoendbeacon [Details](details.md).\n",
		"metrics/details.md": "---\ntype: Reference\ntitle: Revenue details\n---\n# Details\n",
	} {
		path := filepath.Join(sourceRoot, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	loadTestConfig(t, testConfigOptions{knowl: fmt.Sprintf(`workspace:
  path: %s
storage:
  type: sqlite
  sqlite:
    path: .knowl/state.db
scope: local
sources:
  - id: catalog
    type: filesystem
    filesystem:
      root: %s
      include: ["**/*.md"]
      flavor: okf`, strconvQuote(workspaceRoot), strconvQuote(sourceRoot))})

	if _, stderr, err := executeCLICommand(newRootCommand(), []string{migrateCommandName, migrateOKFV02Name}, nil); err != nil {
		t.Fatalf("migrate: %v, stderr=%s", err, stderr)
	}
	if stdout, stderr, err := executeCLICommand(newRootCommand(), []string{sourceCommandName, sourceSyncCommandName, migrationCatalogSourceID}, nil); err != nil || !strings.Contains(stdout, `"changed":true`) {
		t.Fatalf("sync: %v, stdout=%s, stderr=%s", err, stdout, stderr)
	}
	futureIndex := "---\nokf_version: \"0.9\"\n---\n# Future Catalog\n\n* [Metric](metrics/revenue.md)\n"
	if err := os.WriteFile(filepath.Join(sourceRoot, "index.md"), []byte(futureIndex), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeCLICommand(newRootCommand(), []string{sourceCommandName, sourceSyncCommandName, migrationCatalogSourceID}, nil)
	if err != nil || !strings.Contains(stdout, `"code":"okf.version.best_effort"`) || !strings.Contains(stdout, `"observed_version":"0.9"`) {
		t.Fatalf("best-effort version report: %v, stdout=%s, stderr=%s", err, stdout, stderr)
	}
	// A new root command constructs a new Host, exercising persisted projection
	// and canonical workspace recovery rather than reusing the sync process.
	stdout, stderr, err = executeCLICommand(newRootCommand(), []string{retrieveCommandName, retrieveSourceFlag, migrationCatalogSourceID, "Okfendtoendbeacon"}, nil)
	if err != nil || !strings.Contains(stdout, `"type":"Metric"`) ||
		!strings.Contains(stdout, `"trust_tier":"human-reviewed"`) || !strings.Contains(stdout, `"producer":"fixture"`) {
		t.Fatalf("retrieve after restart: %v, stdout=%s, stderr=%s", err, stdout, stderr)
	}
}

func TestReadOnlyCommandsDoNotTriggerOKFMigration(t *testing.T) {
	workspaceRoot := writeLegacyCommandWorkspace(t)
	configureMigrationCommand(t, workspaceRoot)
	pagePath := filepath.Join(workspaceRoot, "wiki", "entities", "legacy.md")
	before, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{retrieveCommandName, "migrationcommandbeacon"}, {sourceCommandName, sourceStatusCommandName, "engineering"}} {
		_, _, _ = executeCLICommand(newRootCommand(), args, nil)
		after, readErr := os.ReadFile(pagePath)
		if readErr != nil || string(after) != string(before) {
			t.Fatalf("read-only command %q mutated legacy page: %v", args, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(workspaceRoot, ".knowl", "migrations", migrateOKFV02Name+".yaml")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("read-only command %q created migration marker: %v", args, statErr)
		}
	}
}

func configureMigrationCommand(t *testing.T, workspaceRoot string) {
	t.Helper()
	loadTestConfig(t, testConfigOptions{knowl: fmt.Sprintf(`workspace:
  path: %s
storage:
  type: sqlite
  sqlite:
    path: .knowl/state.db
scope: local`, strconvQuote(workspaceRoot))})
}

func writeLegacyCommandWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, relative := range []string{"raw", "wiki/entities", ".knowl/staging", ".knowl/recovery", ".knowl/commits"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(relative)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"schema.md":               "# Schema\n",
		"wiki/index.md":           "# Legacy Index\n\n* [Legacy](entities/legacy.md)\n",
		"wiki/log.md":             "# Knowl Update Log\n- legacy record without date\n",
		"wiki/entities/legacy.md": "---\nid: entities/legacy\ntitle: Legacy\ntype: entity\nsource_refs:\n  - raw:legacy@1\n---\n# Legacy\n\nmigrationcommandbeacon body\n",
	}
	for relative, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
