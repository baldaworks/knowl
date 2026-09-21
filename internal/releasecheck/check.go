// Package releasecheck verifies that checked-in and staged distribution
// artifacts describe one Knowl release.
package releasecheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/baldaworks/knowl/internal/releaseinfo"
	"gopkg.in/yaml.v3"
)

const (
	npmPackage  = "@baldaworks/knowl"
	goOSDarwin  = "darwin"
	goOSLinux   = "linux"
	archAMD64   = "amd64"
	archARM64   = "arm64"
	npmCPUX64   = "x64"
	binaryName  = "knowl"
	npxCommand  = "npx"
	yesArgument = "--yes"
	setupName   = "setup"
	runName     = "run"
)

var (
	errPluginContract        = errors.New("plugin release contract mismatch")
	errSkillContract         = errors.New("skill release contract mismatch")
	errOmnidistContract      = errors.New("omnidist release contract mismatch")
	errDocumentationContract = errors.New("release documentation contract mismatch")
	errBinaryContract        = errors.New("release binary contract mismatch")
	errStagedNPMContract     = errors.New("staged npm contract mismatch")
)

var targets = []target{
	{goos: goOSDarwin, goarch: archAMD64, npmOS: goOSDarwin, npmCPU: npmCPUX64, suffix: "darwin-x64", binary: binaryName},
	{goos: goOSDarwin, goarch: archARM64, npmOS: goOSDarwin, npmCPU: archARM64, suffix: "darwin-arm64", binary: binaryName},
	{goos: goOSLinux, goarch: archAMD64, npmOS: goOSLinux, npmCPU: npmCPUX64, suffix: "linux-x64", binary: binaryName},
	{goos: goOSLinux, goarch: archARM64, npmOS: goOSLinux, npmCPU: archARM64, suffix: "linux-arm64", binary: binaryName},
	{goos: "windows", goarch: archAMD64, npmOS: "win32", npmCPU: npmCPUX64, suffix: "win32-x64", binary: "knowl.exe"},
}

type target struct {
	goos   string
	goarch string
	npmOS  string
	npmCPU string
	suffix string
	binary string
}

// Options selects the release identity and optional built artifacts to check.
type Options struct {
	Root      string
	Version   string
	Binary    string
	StagedNPM string
}

// Check verifies checked-in release metadata and any supplied built artifacts.
func Check(options Options) error {
	identity, err := releaseinfo.Parse(options.Version)
	if err != nil {
		return fmt.Errorf("expected stable release version: %w", err)
	}
	if !identity.Release {
		return errors.New("expected stable release version")
	}
	if options.Root == "" {
		return errors.New("repository root is required")
	}
	checks := []func() error{
		func() error { return checkPlugin(options.Root, identity.Version) },
		func() error { return checkSkills(options.Root, identity.Version) },
		func() error { return checkOmnidist(options.Root) },
		func() error { return checkReleaseNotes(options.Root, identity.Version) },
	}
	if options.Binary != "" {
		checks = append(checks, func() error { return checkBinary(options.Binary, identity) })
	}
	if options.StagedNPM != "" {
		checks = append(checks, func() error { return checkStagedNPM(options.StagedNPM, identity.Version) })
	}
	for _, check := range checks {
		if checkErr := check(); checkErr != nil {
			return checkErr
		}
	}
	return nil
}

func checkPlugin(root, version string) error {
	var marketplace struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name     string `json:"name"`
			Category string `json:"category"`
			Source   struct {
				Source string `json:"source"`
				Path   string `json:"path"`
			} `json:"source"`
			Policy struct {
				Installation   string `json:"installation"`
				Authentication string `json:"authentication"`
			} `json:"policy"`
		} `json:"plugins"`
	}
	if err := readJSON(filepath.Join(root, ".agents", "plugins", "marketplace.json"), &marketplace); err != nil {
		return err
	}
	if marketplace.Name != binaryName || len(marketplace.Plugins) != 1 {
		return fmt.Errorf("%w: marketplace identity", errPluginContract)
	}
	plugin := marketplace.Plugins[0]
	if plugin.Name != binaryName || plugin.Category != "Developer Tools" ||
		plugin.Source.Source != "local" || plugin.Source.Path != "./plugins/knowl" ||
		plugin.Policy.Installation != "AVAILABLE" || plugin.Policy.Authentication != "ON_INSTALL" {
		return fmt.Errorf("%w: marketplace entry", errPluginContract)
	}

	var manifest struct {
		Name       string `json:"name"`
		Version    string `json:"version"`
		Skills     string `json:"skills"`
		MCPServers string `json:"mcpServers"`
		License    string `json:"license"`
	}
	if err := readJSON(filepath.Join(root, "plugins", "knowl", ".codex-plugin", "plugin.json"), &manifest); err != nil {
		return err
	}
	if manifest.Name != binaryName || manifest.Version != version || manifest.Skills != "./skills/" ||
		manifest.MCPServers != "./.mcp.json" || manifest.License != "MIT" {
		return fmt.Errorf("%w: plugin version %q does not match %q", errPluginContract, manifest.Version, version)
	}

	var mcp struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := readJSON(filepath.Join(root, "plugins", "knowl", ".mcp.json"), &mcp); err != nil {
		return err
	}
	serverJSON, ok := mcp.Servers[binaryName]
	if !ok || len(mcp.Servers) != 1 {
		return fmt.Errorf("%w: MCP server inventory", errPluginContract)
	}
	var serverFields map[string]json.RawMessage
	if err := json.Unmarshal(serverJSON, &serverFields); err != nil {
		return fmt.Errorf("%w: decode MCP server: %v", errPluginContract, err)
	}
	wantFields := []string{"args", "command", "default_tools_approval_mode", "startup_timeout_sec", "type"}
	if !exactKeys(serverFields, wantFields) {
		return fmt.Errorf("%w: MCP server fields", errPluginContract)
	}
	var server struct {
		Type                string   `json:"type"`
		Command             string   `json:"command"`
		Args                []string `json:"args"`
		StartupTimeout      int      `json:"startup_timeout_sec"`
		DefaultApprovalMode string   `json:"default_tools_approval_mode"`
	}
	if err := json.Unmarshal(serverJSON, &server); err != nil {
		return fmt.Errorf("%w: decode MCP server: %v", errPluginContract, err)
	}
	wantArgs := []string{yesArgument, npmPackage + "@" + version, "mcp", "stdio"}
	if server.Type != "stdio" || server.Command != npxCommand || !reflect.DeepEqual(server.Args, wantArgs) ||
		server.StartupTimeout != 60 || server.DefaultApprovalMode != "writes" {
		return fmt.Errorf("%w: MCP command for release %s", errPluginContract, version)
	}
	return nil
}

func checkSkills(root, version string) error {
	skillsRoot := filepath.Join(root, "plugins", "knowl", "skills")
	entries, err := os.ReadDir(skillsRoot)
	if err != nil {
		return fmt.Errorf("read plugin skills: %w", err)
	}
	gotSkillNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			gotSkillNames = append(gotSkillNames, entry.Name())
		}
	}
	sort.Strings(gotSkillNames)
	if !reflect.DeepEqual(gotSkillNames, []string{runName, setupName}) {
		return fmt.Errorf("%w: skill inventory", errSkillContract)
	}

	pin := npmPackage + "@" + version
	want := map[string]map[string][]string{
		setupName: {
			setupName:  {npxCommand, yesArgument, pin, setupName, "codex"},
			"replace":  {npxCommand, yesArgument, pin, setupName, "codex", "--replace"},
			"validate": {npxCommand, yesArgument, pin, "validate"},
		},
		runName: {
			"local_version":   {binaryName, "version", "--json"},
			"fallback_prefix": {npxCommand, yesArgument, pin},
			"all_sources":     {runName},
			"one_source":      {runName, "--source", "<validated-source-id>"},
		},
	}
	for name, expectedArgv := range want {
		path := filepath.Join(skillsRoot, name, "contract.json")
		var contract struct {
			ReleaseVersion string              `json:"release_version"`
			Argv           map[string][]string `json:"argv"`
			Workflow       struct {
				ValidateBefore         bool     `json:"validate_before"`
				ValidateAfter          bool     `json:"validate_after"`
				ResultJSONRequired     bool     `json:"result_json_required"`
				FailedOperationsField  string   `json:"failed_operations_field"`
				FailOnRunNonzero       bool     `json:"fail_on_run_nonzero"`
				FailOnFailedOperations bool     `json:"fail_on_failed_operations"`
				PreserveUnrelated      bool     `json:"preserve_unrelated_changes"`
				ReportOwnedChanges     bool     `json:"report_owned_changes"`
				ForbiddenSubcommands   []string `json:"forbidden_subcommands"`
			} `json:"workflow"`
		}
		if err := readJSON(path, &contract); err != nil {
			return err
		}
		if contract.ReleaseVersion != version || !reflect.DeepEqual(contract.Argv, expectedArgv) {
			return fmt.Errorf("%w: %s", errSkillContract, name)
		}
		if name == runName && (!contract.Workflow.ValidateBefore || !contract.Workflow.ValidateAfter ||
			!contract.Workflow.ResultJSONRequired || contract.Workflow.FailedOperationsField != "operations.failed" ||
			!contract.Workflow.FailOnRunNonzero || !contract.Workflow.FailOnFailedOperations ||
			!contract.Workflow.PreserveUnrelated || !contract.Workflow.ReportOwnedChanges ||
			!reflect.DeepEqual(contract.Workflow.ForbiddenSubcommands, []string{"start", "mcp"})) {
			return fmt.Errorf("%w: %s workflow", errSkillContract, name)
		}
	}
	return nil
}

func exactKeys[V any](values map[string]V, want []string) bool {
	if len(values) != len(want) {
		return false
	}
	for _, key := range want {
		if _, ok := values[key]; !ok {
			return false
		}
	}
	return true
}

func checkOmnidist(root string) error {
	var config struct {
		Profiles map[string]struct {
			Tool struct {
				Name string `yaml:"name"`
				Main string `yaml:"main"`
			} `yaml:"tool"`
			Version struct {
				Source string `yaml:"source"`
			} `yaml:"version"`
			Targets []struct {
				OS   string `yaml:"os"`
				Arch string `yaml:"arch"`
			} `yaml:"targets"`
			Build struct {
				LDFlags string `yaml:"ldflags"`
			} `yaml:"build"`
			EnabledDistributions []string `yaml:"enabled-distributions"`
			Distributions        map[string]struct {
				Package       string `yaml:"package"`
				Registry      string `yaml:"registry"`
				Access        string `yaml:"access"`
				PublishAuth   string `yaml:"publish-auth"`
				RepositoryURL string `yaml:"repository-url"`
			} `yaml:"distributions"`
		} `yaml:"profiles"`
	}
	path := filepath.Join(root, ".omnidist", "omnidist.yaml")
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(content, &config); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	profile, ok := config.Profiles["default"]
	if !ok || profile.Tool.Name != "knowl" || profile.Tool.Main != "./cmd/knowl" || profile.Version.Source != "git-tag" {
		return fmt.Errorf("%w: tool or version source", errOmnidistContract)
	}
	npm := profile.Distributions["npm"]
	if !reflect.DeepEqual(profile.EnabledDistributions, []string{"npm"}) || len(profile.Distributions) != 1 ||
		npm.Package != npmPackage || npm.Registry != "https://registry.npmjs.org" || npm.Access != "public" ||
		npm.PublishAuth != "token" || npm.RepositoryURL != "git+https://github.com/baldaworks/knowl.git" {
		return fmt.Errorf("%w: npm distribution", errOmnidistContract)
	}
	wantLDFlags := []string{
		"-s",
		"-w",
		"-X",
		"github.com/baldaworks/knowl/internal/releaseinfo.Version=${OMNIDIST_VERSION}",
	}
	if !reflect.DeepEqual(strings.Fields(profile.Build.LDFlags), wantLDFlags) {
		return fmt.Errorf("%w: build flags", errOmnidistContract)
	}
	gotTargets := make([]string, 0, len(profile.Targets))
	for _, configured := range profile.Targets {
		gotTargets = append(gotTargets, configured.OS+"/"+configured.Arch)
	}
	wantTargets := make([]string, 0, len(targets))
	for _, expected := range targets {
		wantTargets = append(wantTargets, expected.goos+"/"+expected.goarch)
	}
	sort.Strings(gotTargets)
	sort.Strings(wantTargets)
	if !reflect.DeepEqual(gotTargets, wantTargets) {
		return fmt.Errorf("%w: targets %v do not match %v", errOmnidistContract, gotTargets, wantTargets)
	}
	return nil
}

func checkReleaseNotes(root, version string) error {
	path := filepath.Join(root, "docs", "releases", "v"+version+".json")
	var metadata struct {
		Version    string `json:"version"`
		NPMPackage string `json:"npm_package"`
	}
	if err := readJSON(path, &metadata); err != nil {
		return err
	}
	if metadata.Version != version || metadata.NPMPackage != npmPackage {
		return fmt.Errorf("%w: %s", errDocumentationContract, version)
	}
	return nil
}

func checkBinary(path string, expected releaseinfo.Identity) error {
	output, err := exec.Command(path, "version", "--json").Output()
	if err != nil {
		return fmt.Errorf("%w: run binary: %v", errBinaryContract, err)
	}
	var actual releaseinfo.Identity
	if err := json.Unmarshal(output, &actual); err != nil {
		return fmt.Errorf("%w: decode version: %v", errBinaryContract, err)
	}
	if actual != expected {
		return fmt.Errorf("%w: identity %#v does not match %#v", errBinaryContract, actual, expected)
	}
	return nil
}

func checkStagedNPM(root, version string) error {
	rootPackage := npmPackage
	var meta struct {
		Name                 string            `json:"name"`
		Version              string            `json:"version"`
		Bin                  map[string]string `json:"bin"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	if err := readJSON(packageJSONPath(root, rootPackage), &meta); err != nil {
		return err
	}
	if meta.Name != rootPackage || meta.Version != version || meta.Bin["knowl"] != "knowl.js" {
		return fmt.Errorf("%w: root package %s@%s", errStagedNPMContract, rootPackage, version)
	}
	wantDependencies := make(map[string]string, len(targets))
	for _, expected := range targets {
		name := npmPackage + "-" + expected.suffix
		wantDependencies[name] = version
		var native struct {
			Name    string   `json:"name"`
			Version string   `json:"version"`
			OS      []string `json:"os"`
			CPU     []string `json:"cpu"`
		}
		if err := readJSON(packageJSONPath(root, name), &native); err != nil {
			return err
		}
		if native.Name != name || native.Version != version ||
			!reflect.DeepEqual(native.OS, []string{expected.npmOS}) ||
			!reflect.DeepEqual(native.CPU, []string{expected.npmCPU}) {
			return fmt.Errorf("%w: native package %s", errStagedNPMContract, name)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name), "bin", expected.binary)); err != nil {
			return fmt.Errorf("%w: inspect binary for %s: %v", errStagedNPMContract, name, err)
		}
	}
	if !reflect.DeepEqual(meta.OptionalDependencies, wantDependencies) {
		return fmt.Errorf("%w: native dependencies", errStagedNPMContract)
	}
	return nil
}

func packageJSONPath(root, name string) string {
	return filepath.Join(root, filepath.FromSlash(name), "package.json")
}

func readJSON(path string, output any) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(content, output); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
