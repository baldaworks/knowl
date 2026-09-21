package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/internal/releaseinfo"
)

const (
	fakeCodexEnabledEnv   = "KNOWL_TEST_FAKE_CODEX"
	fakeCodexFixtureEnv   = "KNOWL_TEST_FAKE_CODEX_FIXTURE"
	fakeCodexCallLogEnv   = "KNOWL_TEST_FAKE_CODEX_CALL_LOG"
	testReleaseVersion    = "v0.6.0"
	fakeCodexArgSeparator = "\x1f"
	marketplaceAddedJSON  = `{"marketplaceName":"knowl","alreadyAdded":false}`
)

type fakeCodexResponse struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr,omitempty"`
	Exit   int    `json:"exit,omitempty"`
}

type fakeCodexFixture struct {
	Responses map[string][]fakeCodexResponse `json:"responses"`
}

func TestSetupCodexFakeExecutable(t *testing.T) {
	if os.Getenv(fakeCodexEnabledEnv) != "1" {
		return
	}
	args := argsAfterSeparator(os.Args)
	fixtureBytes, err := os.ReadFile(os.Getenv(fakeCodexFixtureEnv))
	if err != nil {
		os.Exit(91)
	}
	var fixture fakeCodexFixture
	if err := json.Unmarshal(fixtureBytes, &fixture); err != nil {
		os.Exit(92)
	}
	calls := readFakeCodexCalls(os.Getenv(fakeCodexCallLogEnv))
	occurrence := 0
	for _, call := range calls {
		if reflect.DeepEqual(call, args) {
			occurrence++
		}
	}
	logFile, err := os.OpenFile(os.Getenv(fakeCodexCallLogEnv), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(93)
	}
	if err := json.NewEncoder(logFile).Encode(args); err != nil {
		_ = logFile.Close()
		os.Exit(94)
	}
	if err := logFile.Close(); err != nil {
		os.Exit(95)
	}
	responses := fixture.Responses[strings.Join(args, fakeCodexArgSeparator)]
	if occurrence >= len(responses) {
		os.Exit(96)
	}
	response := responses[occurrence]
	_, _ = os.Stdout.WriteString(response.Stdout)
	_, _ = os.Stderr.WriteString(response.Stderr)
	os.Exit(response.Exit)
}

func TestSetupCodexInstallsAbsentIntegrationWithPinnedSparseArgv(t *testing.T) {
	fixture := newFakeCodexFixture(t, map[string][]fakeCodexResponse{
		fakeCodexKey(codexPluginAction, codexMarketplaceAction, sourceListCommandName, codexJSONFlag): {{Stdout: `{"marketplaces":[]}`}},
		fakeCodexKey(marketplaceAddArgs()...): {{Stdout: marketplaceAddedJSON}},
		fakeCodexKey(pluginListArgs()...): {
			{Stdout: pluginListJSON(nil, []codexPlugin{testPlugin(false, true, "0.6.0")})},
		},
		fakeCodexKey(codexPluginAction, codexAddAction, knowlPluginID, codexJSONFlag): {{Stdout: `{"pluginId":"knowl@knowl","marketplaceName":"knowl","version":"0.6.0"}`}},
	})

	result, err := executeSetupCodex(t, fixture, false)
	if err != nil {
		t.Fatalf("setup codex: %v", err)
	}
	want := setupCodexResult{Version: "0.6.0", Project: "skipped", Marketplace: "added", Plugin: "installed", RestartRequired: true}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	wantCalls := [][]string{
		{codexPluginAction, codexMarketplaceAction, sourceListCommandName, codexJSONFlag},
		marketplaceAddArgs(),
		pluginListArgs(),
		{codexPluginAction, codexAddAction, knowlPluginID, codexJSONFlag},
	}
	assertFakeCodexCalls(t, fixture.callLog, wantCalls)
}

func TestSetupCodexIsNoOpForCompatibleIntegration(t *testing.T) {
	fixture := newFakeCodexFixture(t, map[string][]fakeCodexResponse{
		fakeCodexKey(codexPluginAction, codexMarketplaceAction, sourceListCommandName, codexJSONFlag): {{Stdout: compatibleMarketplaceJSON()}},
		fakeCodexKey(pluginListArgs()...): {
			{Stdout: pluginListJSON([]codexPlugin{testPlugin(true, true, "0.6.0")}, nil)},
		},
		fakeCodexKey(marketplaceAddArgs()...): {{Stdout: `{"marketplaceName":"knowl","alreadyAdded":true}`}},
	})

	result, err := executeSetupCodex(t, fixture, false)
	if err != nil {
		t.Fatalf("setup codex: %v", err)
	}
	if result.Marketplace != setupStatusUnchanged || result.Plugin != setupStatusUnchanged || result.RestartRequired {
		t.Fatalf("unexpected no-op result: %#v", result)
	}
	assertFakeCodexCalls(t, fixture.callLog, [][]string{
		{codexPluginAction, codexMarketplaceAction, sourceListCommandName, codexJSONFlag},
		marketplaceAddArgs(),
		pluginListArgs(),
	})
}

func TestSetupCodexRequiresReplaceForConflictingMarketplace(t *testing.T) {
	fixture := newFakeCodexFixture(t, map[string][]fakeCodexResponse{
		fakeCodexKey(codexPluginAction, codexMarketplaceAction, sourceListCommandName, codexJSONFlag): {{Stdout: `{"marketplaces":[{"name":"knowl","marketplaceSource":{"sourceType":"local","source":"/tmp/not-knowl"}}]}`}},
	})

	_, err := executeSetupCodex(t, fixture, false)
	var boundary *setupBoundaryError
	if !errors.As(err, &boundary) || boundary.Boundary != "marketplace_conflict" {
		t.Fatalf("error = %v, want marketplace_conflict", err)
	}
	assertFakeCodexCalls(t, fixture.callLog, [][]string{{codexPluginAction, codexMarketplaceAction, sourceListCommandName, codexJSONFlag}})
}

func TestSetupCodexReplacesOnlyAfterExplicitAuthorization(t *testing.T) {
	fixture := newFakeCodexFixture(t, map[string][]fakeCodexResponse{
		fakeCodexKey(codexPluginAction, codexMarketplaceAction, sourceListCommandName, codexJSONFlag): {{Stdout: `{"marketplaces":[{"name":"knowl","marketplaceSource":{"sourceType":"local","source":"/tmp/not-knowl"}}]}`}},
		fakeCodexKey(pluginListArgs()...): {
			{Stdout: pluginListJSON([]codexPlugin{testPlugin(true, true, "0.5.0")}, nil)},
			{Stdout: pluginListJSON(nil, []codexPlugin{testPlugin(false, true, "0.6.0")})},
		},
		fakeCodexKey(codexPluginAction, "remove", knowlPluginID, codexJSONFlag):                                {{Stdout: `{"pluginId":"knowl@knowl","name":"knowl","marketplaceName":"knowl"}`}},
		fakeCodexKey(codexPluginAction, codexMarketplaceAction, "remove", knowlMarketplaceName, codexJSONFlag): {{Stdout: `{"marketplaceName":"knowl"}`}},
		fakeCodexKey(marketplaceAddArgs()...):                                                                  {{Stdout: marketplaceAddedJSON}},
		fakeCodexKey(codexPluginAction, codexAddAction, knowlPluginID, codexJSONFlag):                          {{Stdout: `{"pluginId":"knowl@knowl","marketplaceName":"knowl","version":"0.6.0"}`}},
	})

	result, err := executeSetupCodex(t, fixture, true)
	if err != nil {
		t.Fatalf("replace setup codex: %v", err)
	}
	if result.Marketplace != setupStatusReplaced || result.Plugin != setupStatusReplaced || !result.RestartRequired {
		t.Fatalf("unexpected replacement result: %#v", result)
	}
	assertFakeCodexCalls(t, fixture.callLog, [][]string{
		{codexPluginAction, codexMarketplaceAction, sourceListCommandName, codexJSONFlag},
		pluginListArgs(),
		{codexPluginAction, "remove", knowlPluginID, codexJSONFlag},
		{codexPluginAction, codexMarketplaceAction, "remove", knowlMarketplaceName, codexJSONFlag},
		marketplaceAddArgs(),
		pluginListArgs(),
		{codexPluginAction, codexAddAction, knowlPluginID, codexJSONFlag},
	})
}

func TestSetupCodexReportsInspectionBoundaryWithoutLeakingStderr(t *testing.T) {
	fixture := newFakeCodexFixture(t, map[string][]fakeCodexResponse{
		fakeCodexKey(codexPluginAction, codexMarketplaceAction, sourceListCommandName, codexJSONFlag): {{Stderr: "token=top-secret", Exit: 23}},
	})

	_, err := executeSetupCodex(t, fixture, false)
	var boundary *setupBoundaryError
	if !errors.As(err, &boundary) || boundary.Boundary != "marketplace_inspection" {
		t.Fatalf("error = %v, want marketplace_inspection", err)
	}
	var exitError *exec.ExitError
	if !errors.As(boundary.Err, &exitError) || exitError.ExitCode() != 23 {
		t.Fatalf("boundary cause = %T, want exit code 23", boundary.Err)
	}
	if len(exitError.Stderr) != 0 {
		t.Fatalf("boundary retained captured stderr: %q", exitError.Stderr)
	}
}

func TestSetupCodexPluginFailurePreservesProjectFiles(t *testing.T) {
	project := t.TempDir()
	workspace := filepath.Join(project, "knowledge")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	customSchema := []byte("# Custom schema\n")
	if err := os.WriteFile(filepath.Join(workspace, schemaFile), customSchema, 0o600); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	configPath := filepath.Join(project, ".config", appName, "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	customConfig := []byte("custom: true\n")
	if err := os.WriteFile(configPath, customConfig, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	unrelatedPath := filepath.Join(project, "notes.txt")
	unrelated := []byte("user work\n")
	if err := os.WriteFile(unrelatedPath, unrelated, 0o600); err != nil {
		t.Fatalf("write unrelated file: %v", err)
	}

	fixture := newFakeCodexFixture(t, map[string][]fakeCodexResponse{
		fakeCodexKey(codexPluginAction, codexMarketplaceAction, sourceListCommandName, codexJSONFlag): {{Stdout: `{"marketplaces":[]}`}},
		fakeCodexKey(marketplaceAddArgs()...):                                         {{Stdout: marketplaceAddedJSON}},
		fakeCodexKey(pluginListArgs()...):                                             {{Stdout: pluginListJSON(nil, []codexPlugin{testPlugin(false, true, "0.6.0")})}},
		fakeCodexKey(codexPluginAction, codexAddAction, knowlPluginID, codexJSONFlag): {{Stderr: "token=top-secret", Exit: 29}},
	})

	ctx, err := loadConfig(context.Background(), "", "")
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}
	loaded, err := configFromContext(ctx)
	if err != nil {
		t.Fatalf("read loaded config: %v", err)
	}
	loaded.WorkingDir = project
	loaded.Document.Knowl.Workspace.Path = workspace
	command := newSetupCommand()
	command.SetContext(context.WithValue(context.Background(), loadedConfigContextKey{}, loaded))
	originalVersion := releaseinfo.Version
	releaseinfo.Version = testReleaseVersion
	t.Cleanup(func() { releaseinfo.Version = originalVersion })

	_, err = runSetupCodex(command, false, false)
	var boundary *setupBoundaryError
	if !errors.As(err, &boundary) || boundary.Boundary != "plugin_install" {
		t.Fatalf("error = %v, want plugin_install", err)
	}
	var exitError *exec.ExitError
	if !errors.As(boundary.Err, &exitError) || exitError.ExitCode() != 29 || len(exitError.Stderr) != 0 {
		t.Fatalf("boundary cause = %#v, want redacted exit code 29", boundary.Err)
	}
	assertFileBytes(t, filepath.Join(workspace, schemaFile), customSchema)
	assertFileBytes(t, configPath, customConfig)
	assertFileBytes(t, unrelatedPath, unrelated)
	assertFakeCodexCalls(t, fixture.callLog, [][]string{
		{codexPluginAction, codexMarketplaceAction, sourceListCommandName, codexJSONFlag},
		marketplaceAddArgs(),
		pluginListArgs(),
		{codexPluginAction, codexAddAction, knowlPluginID, codexJSONFlag},
	})
}

func TestSetupCodexRejectsDevelopmentBuildBeforeMutation(t *testing.T) {
	originalVersion := releaseinfo.Version
	releaseinfo.Version = releaseinfo.DevelopmentVersion
	t.Cleanup(func() { releaseinfo.Version = originalVersion })
	command := newSetupCommand()
	command.SetArgs([]string{setupCodexCommandName, "--skip-project"})
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	err := command.Execute()
	var boundary *setupBoundaryError
	if !errors.As(err, &boundary) || boundary.Boundary != "release_identity" {
		t.Fatalf("error = %v, want release_identity", err)
	}
}

func TestSetupCodexProjectPreservesExistingManagedFiles(t *testing.T) {
	project := t.TempDir()
	workspace := filepath.Join(project, "knowledge")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	customSchema := []byte("# Custom schema\n")
	if err := os.WriteFile(filepath.Join(workspace, schemaFile), customSchema, 0o600); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	configPath := filepath.Join(project, ".config", appName, "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	customConfig := []byte("custom: true\n")
	if err := os.WriteFile(configPath, customConfig, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ctx, err := loadConfig(context.Background(), "", "")
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}
	loaded, err := configFromContext(ctx)
	if err != nil {
		t.Fatalf("read loaded config: %v", err)
	}
	loaded.WorkingDir = project
	loaded.Document.Knowl.Workspace.Path = workspace
	ctx = context.WithValue(context.Background(), loadedConfigContextKey{}, loaded)

	if err := setupCodexProject(ctx); err != nil {
		t.Fatalf("setup project: %v", err)
	}
	assertFileBytes(t, filepath.Join(workspace, schemaFile), customSchema)
	assertFileBytes(t, configPath, customConfig)
}

type fakeCodexTestFixture struct {
	callLog string
}

func newFakeCodexFixture(t *testing.T, responses map[string][]fakeCodexResponse) fakeCodexTestFixture {
	t.Helper()
	directory := t.TempDir()
	fixturePath := filepath.Join(directory, "fixture.json")
	callLog := filepath.Join(directory, "calls.jsonl")
	content, err := json.Marshal(fakeCodexFixture{Responses: responses})
	if err != nil {
		t.Fatalf("marshal fake Codex fixture: %v", err)
	}
	if err := os.WriteFile(fixturePath, content, 0o600); err != nil {
		t.Fatalf("write fake Codex fixture: %v", err)
	}
	t.Setenv(fakeCodexEnabledEnv, "1")
	t.Setenv(fakeCodexFixtureEnv, fixturePath)
	t.Setenv(fakeCodexCallLogEnv, callLog)
	originalProcess := newCodexProcess
	newCodexProcess = func(ctx context.Context, args ...string) *exec.Cmd {
		helperArgs := append([]string{"-test.run=^TestSetupCodexFakeExecutable$", "--"}, args...)
		return exec.CommandContext(ctx, os.Args[0], helperArgs...)
	}
	t.Cleanup(func() { newCodexProcess = originalProcess })
	return fakeCodexTestFixture{callLog: callLog}
}

func executeSetupCodex(t *testing.T, fixture fakeCodexTestFixture, replace bool) (setupCodexResult, error) {
	t.Helper()
	_ = fixture
	originalVersion := releaseinfo.Version
	releaseinfo.Version = testReleaseVersion
	t.Cleanup(func() { releaseinfo.Version = originalVersion })
	command := newSetupCommand()
	command.SetContext(context.Background())
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	args := []string{setupCodexCommandName, "--skip-project"}
	if replace {
		args = append(args, "--replace")
	}
	command.SetArgs(args)
	if err := command.Execute(); err != nil {
		return setupCodexResult{}, err
	}
	var result setupCodexResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode setup output %q: %v", stdout.String(), err)
	}
	return result, nil
}

func marketplaceAddArgs() []string {
	return []string{
		codexPluginAction, codexMarketplaceAction, codexAddAction, knowlMarketplaceSource,
		"--ref", testReleaseVersion,
		"--sparse", ".agents/plugins",
		"--sparse", "plugins/knowl",
		codexJSONFlag,
	}
}

func pluginListArgs() []string {
	return []string{codexPluginAction, sourceListCommandName, "--marketplace", knowlMarketplaceName, "--available", codexJSONFlag}
}

func testPlugin(installed, enabled bool, version string) codexPlugin {
	return codexPlugin{
		PluginID:        knowlPluginID,
		Name:            "knowl",
		MarketplaceName: knowlMarketplaceName,
		Version:         version,
		Installed:       installed,
		Enabled:         enabled,
	}
}

func compatibleMarketplaceJSON() string {
	return `{"marketplaces":[{"name":"knowl","marketplaceSource":{"sourceType":"git","source":"https://github.com/baldaworks/knowl.git"}}]}`
}

func pluginListJSON(installed, available []codexPlugin) string {
	if installed == nil {
		installed = []codexPlugin{}
	}
	if available == nil {
		available = []codexPlugin{}
	}
	content, err := json.Marshal(codexPluginList{Installed: installed, Available: available})
	if err != nil {
		panic(err)
	}
	return string(content)
}

func fakeCodexKey(args ...string) string {
	return strings.Join(args, fakeCodexArgSeparator)
}

func argsAfterSeparator(args []string) []string {
	for index, arg := range args {
		if arg == "--" {
			return args[index+1:]
		}
	}
	return nil
}

func readFakeCodexCalls(path string) [][]string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	var calls [][]string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var call []string
		if json.Unmarshal(scanner.Bytes(), &call) == nil {
			calls = append(calls, call)
		}
	}
	return calls
}

func assertFakeCodexCalls(t *testing.T, path string, want [][]string) {
	t.Helper()
	got := readFakeCodexCalls(path)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Codex calls = %#v, want %#v", got, want)
	}
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
