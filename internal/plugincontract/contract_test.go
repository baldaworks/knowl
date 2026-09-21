package plugincontract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	pluginName     = "knowl"
	releaseVersion = "0.6.0"
	npxCommand     = "npx"
	yesArgument    = "--yes"
	setupSkillName = "setup"
	runSkillName   = "run"
	mcpCommandName = "mcp"
	stdioTransport = "stdio"
	unrelatedPath  = "README.md"
	configPath     = ".config/knowl/config.yaml"
	ownedWikiPath  = "wiki/catalogs/root/index.md"
	configuredFile = "configured"
)

func TestCodexPluginRuntimeContract(t *testing.T) {
	root := repositoryRoot(t)
	marketplace := readObject(t, filepath.Join(root, ".agents", "plugins", "marketplace.json"))
	manifest := readObject(t, filepath.Join(root, "plugins", "knowl", ".codex-plugin", "plugin.json"))
	mcp := readObject(t, filepath.Join(root, "plugins", "knowl", ".mcp.json"))

	plugins := objectArray(t, marketplace, "plugins")
	if marketplace["name"] != pluginName || len(plugins) != 1 {
		t.Fatalf("unexpected marketplace identity or entries: %#v", marketplace)
	}
	entry := objectValue(t, plugins[0], "marketplace plugin")
	source := object(t, entry, "source")
	policy := object(t, entry, "policy")
	if entry["name"] != pluginName || entry["category"] != "Developer Tools" ||
		!reflect.DeepEqual(source, map[string]any{"source": "local", "path": "./plugins/knowl"}) ||
		!reflect.DeepEqual(policy, map[string]any{"installation": "AVAILABLE", "authentication": "ON_INSTALL"}) {
		t.Fatalf("unexpected marketplace plugin: %#v", entry)
	}

	if manifest["name"] != pluginName || manifest["version"] != releaseVersion ||
		manifest["skills"] != "./skills/" || manifest["mcpServers"] != "./.mcp.json" || manifest["license"] != "MIT" {
		t.Fatalf("unexpected plugin identity or companion paths: %#v", manifest)
	}
	for _, forbidden := range []string{"apps", "hooks", "commands"} {
		if _, found := manifest[forbidden]; found {
			t.Fatalf("plugin manifest contains forbidden field %q", forbidden)
		}
	}

	servers := object(t, mcp, "mcpServers")
	if len(servers) != 1 {
		t.Fatalf("MCP server count = %d, want 1", len(servers))
	}
	server := object(t, servers, pluginName)
	wantArgs := []any{yesArgument, "@baldaworks/knowl@" + releaseVersion, mcpCommandName, stdioTransport}
	if server["type"] != stdioTransport || server["command"] != npxCommand ||
		!reflect.DeepEqual(server["args"], wantArgs) ||
		server["startup_timeout_sec"] != float64(60) ||
		server["default_tools_approval_mode"] != "writes" {
		t.Fatalf("unexpected MCP server contract: %#v", server)
	}
	for _, forbidden := range []string{"cwd", "url", "port", "token", "env"} {
		if _, found := server[forbidden]; found {
			t.Fatalf("MCP server contains forbidden field %q", forbidden)
		}
	}
}

func TestSetupSkillContract(t *testing.T) {
	root := repositoryRoot(t)
	skillRoot := filepath.Join(root, "plugins", pluginName, "skills", "setup")
	contract := readSkillContract(t, filepath.Join(skillRoot, "contract.json"))
	wantArgv := map[string][]string{
		setupSkillName: {npxCommand, yesArgument, "@baldaworks/knowl@" + releaseVersion, setupSkillName, "codex"},
		"replace":      {npxCommand, yesArgument, "@baldaworks/knowl@" + releaseVersion, setupSkillName, "codex", "--replace"},
		"validate":     {npxCommand, yesArgument, "@baldaworks/knowl@" + releaseVersion, "validate"},
	}
	if contract.ReleaseVersion != releaseVersion || !reflect.DeepEqual(contract.Argv, wantArgv) {
		t.Fatalf("unexpected setup skill contract: %#v", contract)
	}

	var metadata struct {
		Interface struct {
			DisplayName      string `yaml:"display_name"`
			ShortDescription string `yaml:"short_description"`
			DefaultPrompt    string `yaml:"default_prompt"`
		} `yaml:"interface"`
	}
	if err := readYAML(t, filepath.Join(skillRoot, "agents", "openai.yaml"), &metadata); err != nil {
		t.Fatalf("decode setup skill UI metadata: %v", err)
	}
	if metadata.Interface.DisplayName != "Knowl Setup" ||
		metadata.Interface.ShortDescription != "Set up Knowl and its Codex plugin" ||
		metadata.Interface.DefaultPrompt != "Use $knowl:setup to set up Knowl in this project." {
		t.Fatalf("unexpected setup skill UI metadata: %#v", metadata.Interface)
	}
}

func TestRunSkillContract(t *testing.T) {
	root := repositoryRoot(t)
	skillsRoot := filepath.Join(root, "plugins", pluginName, "skills")
	entries, err := os.ReadDir(skillsRoot)
	if err != nil {
		t.Fatalf("read skill directories: %v", err)
	}
	var skillNames []string
	for _, entry := range entries {
		if entry.IsDir() {
			skillNames = append(skillNames, entry.Name())
		}
	}
	if !reflect.DeepEqual(skillNames, []string{runSkillName, setupSkillName}) {
		t.Fatalf("plugin skill directories = %v, want [run setup]", skillNames)
	}

	skillRoot := filepath.Join(skillsRoot, "run")
	contract := readSkillContract(t, filepath.Join(skillRoot, "contract.json"))
	wantArgv := map[string][]string{
		"local_version":   {pluginName, "version", "--json"},
		"fallback_prefix": {npxCommand, yesArgument, "@baldaworks/knowl@" + releaseVersion},
		"all_sources":     {runSkillName},
		"one_source":      {runSkillName, "--source", "<validated-source-id>"},
	}
	wantWorkflow := runWorkflowContract{
		ValidateBefore:         true,
		ValidateAfter:          true,
		ResultJSONRequired:     true,
		FailedOperationsField:  "operations.failed",
		FailOnRunNonzero:       true,
		FailOnFailedOperations: true,
		PreserveUnrelated:      true,
		ReportOwnedChanges:     true,
		ForbiddenSubcommands:   []string{"start", mcpCommandName},
	}
	if contract.ReleaseVersion != releaseVersion || !reflect.DeepEqual(contract.Argv, wantArgv) ||
		!reflect.DeepEqual(contract.Workflow, wantWorkflow) {
		t.Fatalf("unexpected run skill contract: %#v", contract)
	}

	var metadata struct {
		Interface struct {
			DisplayName      string `yaml:"display_name"`
			ShortDescription string `yaml:"short_description"`
			DefaultPrompt    string `yaml:"default_prompt"`
		} `yaml:"interface"`
	}
	if err := readYAML(t, filepath.Join(skillRoot, "agents", "openai.yaml"), &metadata); err != nil {
		t.Fatalf("decode run skill UI metadata: %v", err)
	}
	if metadata.Interface.DisplayName != "Knowl Run" ||
		metadata.Interface.ShortDescription != "Run one bounded Knowl maintenance cycle" ||
		metadata.Interface.DefaultPrompt != "Use $knowl:run to process this project knowledge once." {
		t.Fatalf("unexpected run skill UI metadata: %#v", metadata.Interface)
	}
}

func TestRunSkillWorkflowRejectsFailureAndUnrelatedMutationFixtures(t *testing.T) {
	contract := readSkillContract(t, filepath.Join(repositoryRoot(t), "plugins", pluginName, "skills", runSkillName, "contract.json"))
	successResult, err := json.Marshal(runFixtureResult{Operations: &runFixtureOperations{Failed: 0}})
	if err != nil {
		t.Fatalf("encode success fixture: %v", err)
	}
	failedResult, err := json.Marshal(runFixtureResult{Operations: &runFixtureOperations{Failed: 1}})
	if err != nil {
		t.Fatalf("encode failed fixture: %v", err)
	}
	before := map[string]string{
		unrelatedPath: "unrelated user edit",
		configPath:    configuredFile,
		ownedWikiPath: "old",
	}
	after := map[string]string{
		unrelatedPath: "unrelated user edit",
		configPath:    configuredFile,
		ownedWikiPath: "new",
	}
	owned := map[string]bool{
		configPath:    true,
		ownedWikiPath: true,
	}

	tests := []struct {
		name              string
		initialValidation bool
		exitCode          int
		result            []byte
		finalValidation   bool
		after             map[string]string
		wantAccepted      bool
	}{
		{name: "success", initialValidation: true, result: successResult, finalValidation: true, after: after, wantAccepted: true},
		{name: "failed initial validation", result: successResult, finalValidation: true, after: after},
		{name: "nonzero run", initialValidation: true, exitCode: 1, result: successResult, finalValidation: true, after: after},
		{name: "invalid result JSON", initialValidation: true, result: []byte("not-json"), finalValidation: true, after: after},
		{name: "missing operation result", initialValidation: true, result: []byte(`{}`), finalValidation: true, after: after},
		{name: "failed operation", initialValidation: true, result: failedResult, finalValidation: true, after: after},
		{name: "failed final validation", initialValidation: true, result: successResult, after: after},
		{name: "unrelated file changed", initialValidation: true, result: successResult, finalValidation: true, after: map[string]string{
			unrelatedPath: "rewritten",
			configPath:    configuredFile,
			ownedWikiPath: "new",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			accepted := evaluateRunFixture(contract.Workflow, test.initialValidation, test.exitCode, test.result, test.finalValidation, before, test.after, owned)
			if accepted != test.wantAccepted {
				t.Fatalf("accepted = %t, want %t", accepted, test.wantAccepted)
			}
		})
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve contract test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readObject(t *testing.T, path string) map[string]any {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var output map[string]any
	if err := json.Unmarshal(content, &output); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return output
}

type skillContract struct {
	ReleaseVersion string              `json:"release_version"`
	Argv           map[string][]string `json:"argv"`
	Workflow       runWorkflowContract `json:"workflow"`
}

type runWorkflowContract struct {
	ValidateBefore         bool     `json:"validate_before"`
	ValidateAfter          bool     `json:"validate_after"`
	ResultJSONRequired     bool     `json:"result_json_required"`
	FailedOperationsField  string   `json:"failed_operations_field"`
	FailOnRunNonzero       bool     `json:"fail_on_run_nonzero"`
	FailOnFailedOperations bool     `json:"fail_on_failed_operations"`
	PreserveUnrelated      bool     `json:"preserve_unrelated_changes"`
	ReportOwnedChanges     bool     `json:"report_owned_changes"`
	ForbiddenSubcommands   []string `json:"forbidden_subcommands"`
}

type runFixtureResult struct {
	Operations *runFixtureOperations `json:"operations"`
}

type runFixtureOperations struct {
	Failed int `json:"failed"`
}

func evaluateRunFixture(
	contract runWorkflowContract,
	initialValidation bool,
	exitCode int,
	output []byte,
	finalValidation bool,
	before, after map[string]string,
	owned map[string]bool,
) bool {
	if !contract.ValidateBefore || !contract.ValidateAfter || !initialValidation {
		return false
	}
	if contract.FailOnRunNonzero && exitCode != 0 {
		return false
	}
	var result runFixtureResult
	if err := json.Unmarshal(output, &result); err != nil && contract.ResultJSONRequired {
		return false
	}
	if result.Operations == nil || contract.FailedOperationsField != "operations.failed" {
		return false
	}
	if contract.FailOnFailedOperations && result.Operations.Failed > 0 {
		return false
	}
	if contract.ValidateAfter && !finalValidation {
		return false
	}
	if contract.PreserveUnrelated && unrelatedFilesChanged(before, after, owned) {
		return false
	}
	return true
}

func unrelatedFilesChanged(before, after map[string]string, owned map[string]bool) bool {
	for path, beforeContent := range before {
		if !owned[path] && after[path] != beforeContent {
			return true
		}
	}
	for path := range after {
		if !owned[path] {
			if _, found := before[path]; !found {
				return true
			}
		}
	}
	return false
}

func readSkillContract(t *testing.T, path string) skillContract {
	t.Helper()
	var contract skillContract
	if err := readJSON(t, path, &contract); err != nil {
		t.Fatal(err)
	}
	return contract
}

func readJSON(t *testing.T, path string, output any) error {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(content, output)
}

func readYAML(t *testing.T, path string, output any) error {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(content, output)
}

func object(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	return objectValue(t, parent[key], key)
}

func objectValue(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object: %#v", label, value)
	}
	return result
}

func objectArray(t *testing.T, parent map[string]any, key string) []any {
	t.Helper()
	result, ok := parent[key].([]any)
	if !ok {
		t.Fatalf("%s is not an array: %#v", key, parent[key])
	}
	return result
}
