package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
)

func TestBootstrapWikiCreatesNormalizedWorkspace(t *testing.T) {
	clearKnowlEnv(t)
	workdir := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "Home.md"), []byte("---\ntags:\n  - imported\n---\n# Home\n\nSee [Guide](guide.md)\n"), 0o600); err != nil {
		t.Fatalf("write source page: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "guide.md"), []byte("# Guide\n"), 0o600); err != nil {
		t.Fatalf("write source guide: %v", err)
	}

	t.Chdir(workdir)
	stdout, stderr, err := executeCLICommand(newRootCommand(), []string{bootstrapCommandName, bootstrapWikiName, sourceDir}, nil)
	if err != nil {
		t.Fatalf("bootstrap wiki Execute() error: %v, stderr=%s", err, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("bootstrap stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "bootstrapped Knowl workspace") {
		t.Fatalf("bootstrap stderr missing zerolog summary:\n%s", stderr)
	}
	if !strings.Contains(stderr, "markdown_files=2") {
		t.Fatalf("bootstrap stderr missing markdown_files field:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(workdir, ".config", appName, "config.yaml")); err != nil {
		t.Fatalf("expected config artifact: %v", err)
	}
	if err := validateWorkspace(workdir); err != nil {
		t.Fatalf("validate bootstrapped workspace: %v", err)
	}
	workspace, err := contentfs.New(workdir)
	if err != nil {
		t.Fatalf("open bootstrapped workspace: %v", err)
	}
	inspection, err := workspace.Inspect(context.Background(), "local")
	if err != nil {
		t.Fatalf("inspect bootstrapped workspace: %v", err)
	}
	if len(inspection.Snapshot.Pages) != 2 {
		t.Fatalf("bootstrapped pages = %d, want 2", len(inspection.Snapshot.Pages))
	}
	if len(inspection.RawSources) != 2 {
		t.Fatalf("bootstrapped raw sources = %d, want 2", len(inspection.RawSources))
	}

	content, err := os.ReadFile(filepath.Join(workdir, workspaceWikiDir, "notes", "Home.md"))
	if err != nil {
		t.Fatalf("read canonical page: %v", err)
	}
	for _, want := range []string{
		"id: notes/Home",
		"title: Home",
		"type: note",
		"source_refs:",
		"bootstrap_wiki:Home.md@",
		"tags:",
		"- imported",
	} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("canonical page missing %q:\n%s", want, content)
		}
	}

	indexContent, err := os.ReadFile(filepath.Join(workdir, indexFile))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !strings.Contains(string(indexContent), "[[notes/Home|Home]]") || !strings.Contains(string(indexContent), "[[notes/guide|Guide]]") {
		t.Fatalf("index missing bootstrapped pages:\n%s", indexContent)
	}
}

func TestBootstrapObsidianRewritesWikiLinksAndCopiesAssets(t *testing.T) {
	clearKnowlEnv(t)
	workdir := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourceDir, ".obsidian"), 0o700); err != nil {
		t.Fatalf("create obsidian metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, ".obsidian", "workspace.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write obsidian metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "Alpha.md"), []byte("# Alpha\n\n[[Beta|Second]]\n![[diagram.png]]\n"), 0o600); err != nil {
		t.Fatalf("write alpha note: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "Beta.md"), []byte("# Beta\n"), 0o600); err != nil {
		t.Fatalf("write beta note: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "diagram.png"), []byte("png"), 0o600); err != nil {
		t.Fatalf("write diagram asset: %v", err)
	}

	t.Chdir(workdir)
	stdout, stderr, err := executeCLICommand(newRootCommand(), []string{bootstrapCommandName, bootstrapObsidianName, sourceDir}, nil)
	if err != nil {
		t.Fatalf("bootstrap obsidian Execute() error: %v, stderr=%s", err, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("bootstrap obsidian stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "bootstrapped Knowl workspace") {
		t.Fatalf("bootstrap obsidian stderr missing zerolog summary:\n%s", stderr)
	}

	alphaContent, err := os.ReadFile(filepath.Join(workdir, workspaceWikiDir, "notes", "Alpha.md"))
	if err != nil {
		t.Fatalf("read alpha page: %v", err)
	}
	for _, want := range []string{
		"[[notes/Beta|Second]]",
		"![](diagram.png)",
		"bootstrap_obsidian:Alpha.md@",
	} {
		if !strings.Contains(string(alphaContent), want) {
			t.Fatalf("bootstrapped obsidian page missing %q:\n%s", want, alphaContent)
		}
	}
	if _, err := os.Stat(filepath.Join(workdir, workspaceWikiDir, "notes", "diagram.png")); err != nil {
		t.Fatalf("expected copied asset: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, workspaceWikiDir, ".obsidian")); !os.IsNotExist(err) {
		t.Fatalf("obsidian metadata directory should not be copied, stat err = %v", err)
	}

	workspace, err := contentfs.New(workdir)
	if err != nil {
		t.Fatalf("open bootstrapped workspace: %v", err)
	}
	snapshot, err := workspace.Snapshot(context.Background(), "local")
	if err != nil {
		t.Fatalf("snapshot bootstrapped workspace: %v", err)
	}
	found := false
	for _, link := range snapshot.Links {
		if link.From == "notes/Alpha" && link.To == "notes/Beta" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected rewritten obsidian wiki link in snapshot: %#v", snapshot.Links)
	}
}
