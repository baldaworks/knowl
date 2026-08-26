package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestBootstrapWikiCreatesRawBackedWorkspace(t *testing.T) {
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
	configContent, err := os.ReadFile(filepath.Join(workdir, ".config", appName, "config.yaml"))
	if err != nil {
		t.Fatalf("read bootstrap config: %v", err)
	}
	for _, want := range []string{"runtime:", "providers:", "provider: opencode", "id: bootstrap-wiki", "type: filesystem", "flavor: markdown", sourceDir} {
		if !strings.Contains(string(configContent), want) {
			t.Fatalf("bootstrap config missing %q:\n%s", want, configContent)
		}
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
	if len(inspection.Snapshot.Pages) != 0 {
		t.Fatalf("bootstrap copied source pages into LLM wiki: %#v", inspection.Snapshot.Pages)
	}
	if len(inspection.RawSources) != 2 {
		t.Fatalf("bootstrapped raw sources = %d, want 2", len(inspection.RawSources))
	}

	if _, err := os.Stat(filepath.Join(workdir, workspaceWikiDir, "sources", string(bootstrapWikiSourceID))); !os.IsNotExist(err) {
		t.Fatalf("bootstrap created source mirror directory: %v", err)
	}
	foundHome := false
	for _, record := range inspection.RawSources {
		if record.Source.SourceDocument.DocumentID != "Home.md" {
			continue
		}
		content, readErr := workspace.ReadSource(context.Background(), record.Source, knowl.ReadLimits{})
		if readErr != nil || !strings.Contains(string(content), "tags:") || !strings.Contains(string(content), "See [Guide]") {
			t.Fatalf("bootstrap Home raw = %q, %v", content, readErr)
		}
		foundHome = true
	}
	if !foundHome {
		t.Fatal("bootstrap Home raw revision missing")
	}

	indexContent, err := os.ReadFile(filepath.Join(workdir, indexFile))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if string(indexContent) != defaultIndex {
		t.Fatalf("bootstrap changed curated index:\n%s", indexContent)
	}

	statusOut, statusErr, err := executeCLICommand(newRootCommand(), []string{sourceCommandName, sourceStatusCommandName, string(bootstrapWikiSourceID)}, nil)
	if err != nil {
		t.Fatalf("source status after bootstrap error: %v, stderr=%s", err, statusErr)
	}
	if !strings.Contains(statusOut, `"source_id":"bootstrap-wiki"`) || !strings.Contains(statusOut, `"status":"succeeded"`) {
		t.Fatalf("source status after bootstrap = %s", statusOut)
	}
}

func TestBootstrapObsidianPreservesRawLinksAndAssets(t *testing.T) {
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

	workspace, err := contentfs.New(workdir)
	if err != nil {
		t.Fatalf("open bootstrapped workspace: %v", err)
	}
	inspection, err := workspace.Inspect(context.Background(), "local")
	if err != nil {
		t.Fatalf("inspect bootstrapped workspace: %v", err)
	}
	if len(inspection.Snapshot.Pages) != 0 || len(inspection.RawSources) != 3 {
		t.Fatalf("Obsidian bootstrap = pages:%d raw:%d", len(inspection.Snapshot.Pages), len(inspection.RawSources))
	}
	foundAlpha, foundAsset := false, false
	for _, record := range inspection.RawSources {
		content, readErr := workspace.ReadSource(context.Background(), record.Source, knowl.ReadLimits{})
		if readErr != nil {
			t.Fatal(readErr)
		}
		switch record.Source.SourceDocument.DocumentID {
		case "Alpha.md":
			foundAlpha = strings.Contains(string(content), "[[Beta|Second]]") && strings.Contains(string(content), "![[diagram.png]]")
		case "diagram.png":
			foundAsset = string(content) == "png"
		}
	}
	if !foundAlpha || !foundAsset {
		t.Fatalf("Obsidian raw preservation = alpha:%v asset:%v", foundAlpha, foundAsset)
	}
	if _, err := os.Stat(filepath.Join(workdir, workspaceWikiDir, "sources", string(bootstrapObsidianSourceID))); !os.IsNotExist(err) {
		t.Fatalf("Obsidian bootstrap created source mirror directory: %v", err)
	}
}

func TestBootstrapRejectsNonFreshWorkspaceBeforeMutation(t *testing.T) {
	clearKnowlEnv(t)
	workdir := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, workspaceWikiDir), 0o700); err != nil {
		t.Fatal(err)
	}
	curatedPath := filepath.Join(workdir, workspaceWikiDir, "curated.md")
	const curated = "# Curated\n"
	if err := os.WriteFile(curatedPath, []byte(curated), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "Page.md"), []byte("# Page\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(workdir)
	_, _, err := executeCLICommand(newRootCommand(), []string{bootstrapCommandName, bootstrapWikiName, sourceDir}, nil)
	if err == nil || !strings.Contains(err.Error(), "not fresh") {
		t.Fatalf("bootstrap error = %v, want freshness rejection", err)
	}
	content, readErr := os.ReadFile(curatedPath)
	if readErr != nil || string(content) != curated {
		t.Fatalf("curated content after rejection = %q, %v", content, readErr)
	}
	for _, path := range []string{
		filepath.Join(workdir, ".config"),
		filepath.Join(workdir, ".knowl"),
		filepath.Join(workdir, "raw"),
		filepath.Join(workdir, workspaceWikiDir, "sources"),
	} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("bootstrap freshness rejection created %q: %v", path, statErr)
		}
	}
}

func TestBootstrapPreservesExistingConfig(t *testing.T) {
	clearKnowlEnv(t)
	workdir := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "Page.md"), []byte("# Page\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(workdir, ".config", appName, "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := "runtime:\n  providers:\n    opencode:\n      type: opencode_acp\n      opencode_acp:\n        model: opencode/big-pickle\nknowl:\n  provider: opencode\n  workspace:\n    path: " + workdir + "\n  storage:\n    type: sqlite\n    sqlite:\n      path: .knowl/knowl.sqlite\n  scope: local\n# operator-owned marker\n"
	if err := os.WriteFile(configPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(workdir)
	_, stderr, err := executeCLICommand(newRootCommand(), []string{bootstrapCommandName, bootstrapWikiName, sourceDir}, nil)
	if err != nil {
		t.Fatalf("bootstrap with existing config error: %v, stderr=%s", err, stderr)
	}
	content, err := os.ReadFile(configPath)
	if err != nil || string(content) != existing {
		t.Fatalf("existing config changed:\n%s\nerror=%v", content, err)
	}
}

func TestBootstrapRejectsMissingProviderBeforeWorkspaceMutation(t *testing.T) {
	clearKnowlEnv(t)
	workdir := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "Page.md"), []byte("# Page\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(workdir, ".config", appName, "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "knowl:\n  provider: \"\"\n  workspace:\n    path: " + workdir + "\n  storage:\n    type: sqlite\n    sqlite:\n      path: .knowl/knowl.sqlite\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(workdir)
	_, _, err := executeCLICommand(newRootCommand(), []string{bootstrapCommandName, bootstrapWikiName, sourceDir}, nil)
	if err == nil || !strings.Contains(err.Error(), "knowl.provider is required") {
		t.Fatalf("bootstrap error = %v, want required provider", err)
	}
	for _, path := range []string{
		filepath.Join(workdir, schemaFile),
		filepath.Join(workdir, workspaceWikiDir),
		filepath.Join(workdir, "raw"),
		filepath.Join(workdir, ".knowl"),
	} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("provider validation created %q: %v", path, statErr)
		}
	}
}

func TestBootstrapRejectsAssetOnlySourceBeforeCanonicalContent(t *testing.T) {
	clearKnowlEnv(t)
	workdir := t.TempDir()
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "asset.bin"), []byte("asset"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(workdir)
	_, _, err := executeCLICommand(newRootCommand(), []string{bootstrapCommandName, bootstrapWikiName, sourceDir}, nil)
	if err == nil || !strings.Contains(err.Error(), "contains no Markdown files") {
		t.Fatalf("bootstrap error = %v, want empty Markdown rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(workdir, workspaceWikiDir, "sources", string(bootstrapWikiSourceID), "asset.bin")); !os.IsNotExist(statErr) {
		t.Fatalf("asset-only bootstrap created canonical content: %v", statErr)
	}
}

func TestBootstrapRejectsSymlinkedSourceWorkspaceOverlap(t *testing.T) {
	clearKnowlEnv(t)
	workdir := t.TempDir()
	containedSource := filepath.Join(workdir, "source")
	if err := os.MkdirAll(containedSource, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(containedSource, "Page.md"), []byte("# Page\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkRoot := t.TempDir()
	linkedSource := filepath.Join(linkRoot, "linked-source")
	if err := os.Symlink(containedSource, linkedSource); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	t.Chdir(workdir)
	_, _, err := executeCLICommand(newRootCommand(), []string{bootstrapCommandName, bootstrapWikiName, linkedSource}, nil)
	if err == nil || !strings.Contains(err.Error(), "must be separate") {
		t.Fatalf("bootstrap error = %v, want canonical overlap rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(workdir, ".knowl")); !os.IsNotExist(statErr) {
		t.Fatalf("overlap rejection created state: %v", statErr)
	}
}
