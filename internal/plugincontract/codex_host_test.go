package plugincontract_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInstalledCodexLoadsPluginMCPForProjectWorkingDirectory(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("Codex CLI is not installed")
	}
	probe := exec.Command(codex, "plugin", "--help")
	probe.Env = isolatedCodexEnv(t.TempDir())
	if err := probe.Run(); err != nil {
		t.Skip("installed Codex does not support plugins")
	}

	codexHome := t.TempDir()
	project := t.TempDir()
	root := repositoryRoot(t)
	var marketplace struct {
		Name          string `json:"marketplaceName"`
		InstalledRoot string `json:"installedRoot"`
		AlreadyAdded  bool   `json:"alreadyAdded"`
	}
	runCodexJSON(t, codex, codexHome, project, &marketplace, "plugin", "marketplace", "add", root, "--json")
	if marketplace.Name != pluginName || marketplace.InstalledRoot != root || marketplace.AlreadyAdded {
		t.Fatalf("unexpected marketplace result: %#v", marketplace)
	}

	var installed struct {
		PluginID        string `json:"pluginId"`
		MarketplaceName string `json:"marketplaceName"`
		Version         string `json:"version"`
		InstalledPath   string `json:"installedPath"`
	}
	runCodexJSON(t, codex, codexHome, project, &installed, "plugin", "add", "knowl@knowl", "--json")
	if installed.PluginID != "knowl@knowl" || installed.MarketplaceName != pluginName || installed.Version != releaseVersion {
		t.Fatalf("unexpected plugin install result: %#v", installed)
	}
	cacheRelative, err := filepath.Rel(codexHome, installed.InstalledPath)
	if err != nil || filepath.IsAbs(cacheRelative) || cacheRelative == ".." {
		t.Fatalf("installed plugin path %q is outside isolated Codex home %q", installed.InstalledPath, codexHome)
	}
	firstComponent := cacheRelative
	if separator := strings.IndexRune(cacheRelative, filepath.Separator); separator >= 0 {
		firstComponent = cacheRelative[:separator]
	}
	if firstComponent == ".." {
		t.Fatalf("installed plugin path %q is outside isolated Codex home %q", installed.InstalledPath, codexHome)
	}

	var plugins struct {
		Installed []struct {
			PluginID string `json:"pluginId"`
			Version  string `json:"version"`
			Enabled  bool   `json:"enabled"`
		} `json:"installed"`
		Available []any `json:"available"`
	}
	runCodexJSON(t, codex, codexHome, project, &plugins, "plugin", "list", "--marketplace", pluginName, "--json")
	if len(plugins.Installed) != 1 || plugins.Installed[0].PluginID != "knowl@knowl" ||
		plugins.Installed[0].Version != releaseVersion || !plugins.Installed[0].Enabled || len(plugins.Available) != 0 {
		t.Fatalf("unexpected installed plugin inventory: %#v", plugins)
	}

	var servers []struct {
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
		Transport struct {
			Type    string   `json:"type"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Cwd     *string  `json:"cwd"`
		} `json:"transport"`
	}
	runCodexJSON(t, codex, codexHome, project, &servers, mcpCommandName, "list", "--json")
	wantArgs := []string{"--yes", "@baldaworks/knowl@" + releaseVersion, mcpCommandName, stdioTransport}
	if len(servers) != 1 || servers[0].Name != pluginName || !servers[0].Enabled ||
		servers[0].Transport.Type != stdioTransport || servers[0].Transport.Command != "npx" ||
		!reflect.DeepEqual(servers[0].Transport.Args, wantArgs) || servers[0].Transport.Cwd != nil {
		t.Fatalf("unexpected effective MCP inventory: %#v", servers)
	}
}

func runCodexJSON(t *testing.T, binary, codexHome, project string, output any, args ...string) {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Dir = project
	command.Env = isolatedCodexEnv(codexHome)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("Codex command %v failed: %v; stderr=%q", args, err, stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), output); err != nil {
		t.Fatalf("decode Codex command %v output: %v", args, err)
	}
}

func isolatedCodexEnv(codexHome string) []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		key, _, found := strings.Cut(entry, "=")
		if found && key == "CODEX_HOME" {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "CODEX_HOME="+codexHome)
}
