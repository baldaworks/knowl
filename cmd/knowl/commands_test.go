package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/internal/httpapi/trustedrequest"
	"github.com/baldaworks/knowl/pkg/knowl"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/appconfig"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const (
	knownProviderID = "known"
	commandHelpFlag = "--help"
)

func TestInitWorkspaceIsIdempotent(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	if err := initWorkspace(workspace); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	if err := initWorkspace(workspace); err != nil {
		t.Fatalf("re-init workspace: %v", err)
	}
	if err := validateWorkspace(workspace); err != nil {
		t.Fatalf("validate initialized workspace: %v", err)
	}
	for _, relative := range []string{schemaFile, indexFile, logFile} {
		if _, err := os.Stat(filepath.Join(workspace, relative)); err != nil {
			t.Errorf("expected initialized file %q: %v", relative, err)
		}
	}
}

func TestStoreDriverSelection(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "default", want: defaultStore},
		{name: postgresStore, value: postgresStore, want: postgresStore},
		{name: "unsupported", value: "mysql", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), loadedConfigContextKey{}, loadedConfig{
				WorkingDir: t.TempDir(),
				Document: knowlConfigDocument{Knowl: AppConfig{Storage: StorageConfig{
					Type:   test.value,
					SQLite: &SQLiteConfig{},
				}}},
			})
			if test.value == postgresStore {
				loaded := ctx.Value(loadedConfigContextKey{}).(loadedConfig)
				loaded.Document.Knowl.Storage.SQLite = nil
				loaded.Document.Knowl.Storage.Postgres = &PostgresConfig{DSN: "postgres://localhost/knowl"}
				ctx = context.WithValue(context.Background(), loadedConfigContextKey{}, loaded)
			}
			got, err := storeDriver(ctx)
			if test.wantErr {
				if err == nil {
					t.Fatal("storeDriver() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("storeDriver() error: %v", err)
			}
			if got != test.want {
				t.Fatalf("storeDriver() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRootExposesCurrentLifecycleCommands(t *testing.T) {
	root := newRootCommand()
	want := map[string]bool{
		initCommandName:      true,
		validateCommandName:  true,
		bootstrapCommandName: true,
		startCommandName:     true,
		ingestCommandName:    true,
		queryCommandName:     true,
		operationCommandName: true,
	}
	for _, command := range root.Commands() {
		delete(want, command.Name())
	}
	if len(want) != 0 {
		t.Fatalf("missing lifecycle commands: %v", want)
	}
}

func TestRootHelpExplainsSupportedLocalWorkflow(t *testing.T) {
	t.Parallel()

	output := commandHelpOutput(t, newRootCommand())
	for _, want := range []string{
		"Supported local workflow:",
		"knowl bootstrap wiki <path>",
		"knowl bootstrap obsidian <path>",
		"knowl query <text>",
		"knowl ingest --input request.json",
		"knowl operation <operation-id>",
		startCommandUsage,
		"Bootstrap creates a Knowl-owned workspace",
		"retained loopback HTTP/OpenAPI service mode",
		"same KISS contract for retrieve, ingest, operation, and health",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("root help missing %q in output:\n%s", want, output)
		}
	}
	for _, unwanted := range []string{
		"knowl search <text>",
		"knowl lint",
		"knowl page <page-id>",
		"knowl page links <page-id>",
		"review/apply",
	} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("root help unexpectedly contains %q in output:\n%s", unwanted, output)
		}
	}
}

func TestImplementedWorkflowHelpDescribesCLIInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cmd       *cobra.Command
		args      []string
		wantParts []string
	}{
		{
			name: "bootstrap wiki",
			cmd:  newBootstrapCommand(),
			args: []string{bootstrapWikiName, commandHelpFlag},
			wantParts: []string{
				"<path>",
				"fresh Knowl workspace",
				"wiki/notes/**",
			},
		},
		{
			name: "query",
			cmd:  newQueryCommand(),
			wantParts: []string{
				"positional arguments",
				workflowJSONStdoutHelp,
			},
		},
		{
			name: "ingest",
			cmd:  newIngestCommand(),
			wantParts: []string{
				"canonical JSON request body",
				workflowJSONStdoutHelp,
			},
		},
		{
			name: "operation",
			cmd:  newOperationCommand(),
			wantParts: []string{
				"durable operation ID",
				workflowJSONStdoutHelp,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			test.cmd.SetOut(&output)
			test.cmd.SetErr(&output)
			if len(test.args) == 0 {
				test.cmd.SetArgs([]string{commandHelpFlag})
			} else {
				test.cmd.SetArgs(test.args)
			}
			if err := test.cmd.Execute(); err != nil {
				t.Fatalf("%s help Execute() error: %v", test.name, err)
			}
			for _, want := range test.wantParts {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("%s help missing %q in output:\n%s", test.name, want, output.String())
				}
			}
		})
	}
}

func TestWorkflowCommandTreeCoversCurrentLocalSurface(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cmd       *cobra.Command
		wantShort string
		wantSubs  []string
	}{
		{
			name:      bootstrapCommandName,
			cmd:       newBootstrapCommand(),
			wantShort: "Bootstrap a Knowl workspace from an existing Markdown wiki or Obsidian vault",
			wantSubs:  []string{bootstrapWikiName, bootstrapObsidianName},
		},
		{
			name:      queryCommandName,
			cmd:       newQueryCommand(),
			wantShort: "Retrieve bounded evidence from Knowl",
			wantSubs:  nil,
		},
		{
			name:      ingestCommandName,
			cmd:       newIngestCommand(),
			wantShort: "Submit one source to the Knowl ingest pipeline",
			wantSubs:  nil,
		},
		{
			name:      operationCommandName,
			cmd:       newOperationCommand(),
			wantShort: "Read one durable operation status",
			wantSubs:  nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := test.cmd.Short; got != test.wantShort {
				t.Fatalf("%s short = %q, want %q", test.name, got, test.wantShort)
			}
			for _, want := range test.wantSubs {
				found := false
				for _, command := range test.cmd.Commands() {
					if command.Name() == want {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("%s is missing subcommand %q", test.name, want)
				}
			}
		})
	}
}

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

func TestLocalWorkflowRunnerExecutesInjectedHostRequest(t *testing.T) {
	original := newLocalWorkflowSession
	t.Cleanup(func() { newLocalWorkflowSession = original })

	var (
		gotMethod  string
		gotPath    string
		gotBody    string
		gotAuth    string
		gotTrusted bool
	)
	host := &stubLocalWorkflowHost{
		handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			gotMethod = request.Method
			gotPath = request.URL.Path
			content, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			gotBody = string(content)
			gotAuth = request.Header.Get("Authorization")
			gotTrusted = trustedrequest.IsMarked(request.Context())
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(`{"ok":true}`))
		}),
	}
	newLocalWorkflowSession = func(context.Context) (localWorkflowSession, error) {
		return localWorkflowSession{
			Host:            host,
			ShutdownTimeout: time.Second,
		}, nil
	}

	response, err := newLocalWorkflowRunner().Execute(context.Background(), localWorkflowRequest{
		Method: http.MethodPost,
		Path:   publicIngestPath,
		Body:   []byte(`{"hello":"world"}`),
		Headers: http.Header{
			"Authorization": []string{"Bearer test-token"},
			"Content-Type":  []string{"application/json"},
		},
	})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if host.startCalls != 1 {
		t.Fatalf("Start() calls = %d, want 1", host.startCalls)
	}
	if host.stopCalls != 1 {
		t.Fatalf("Stop() calls = %d, want 1", host.stopCalls)
	}
	if gotMethod != http.MethodPost || gotPath != publicIngestPath {
		t.Fatalf("request = (%s %s), want (%s %s)", gotMethod, gotPath, http.MethodPost, publicIngestPath)
	}
	if gotBody != `{"hello":"world"}` {
		t.Fatalf("body = %q, want %q", gotBody, `{"hello":"world"}`)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer test-token")
	}
	if !gotTrusted {
		t.Fatal("request context was not marked trusted")
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusCreated)
	}
	if got := strings.TrimSpace(string(response.Body)); got != `{"ok":true}` {
		t.Fatalf("response body = %q, want %q", got, `{"ok":true}`)
	}
}

func TestIngestCommandAutoAppliesTrustedLocalWorkflowWithoutOperatorToken(t *testing.T) {
	fixture := newCommandWorkflowFixture(t, true)
	withLocalWorkflowSessionFactory(t, fixture.newSessionFactory(t))

	inputPath := writeJSONFixture(t, knowlapi.IngestRequest{
		Content:        pointerTo(smokeSourceText),
		Origin:         pointerTo(smokeSourceID),
		IdempotencyKey: pointerTo(smokeSourceVersion),
	})

	stdout, stderr, err := executeCLICommand(newIngestCommand(), []string{workflowInputFlagUsage, inputPath}, nil)
	if err != nil {
		t.Fatalf("ingest Execute() error: %v", err)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	var result knowlapi.IngestResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode ingest output: %v", err)
	}
	if result.Status != knowlapi.IngestResultStatusCompleted {
		t.Fatalf("ingest operation status = %q, want %q", result.Status, knowlapi.IngestResultStatusCompleted)
	}
	if _, err := os.Stat(fixture.pagePath(smokePagePath)); err != nil {
		t.Fatalf("committed page missing: %v", err)
	}
}

func TestReadCommandsReturnStructuredJSON(t *testing.T) {
	fixture, ingest := prepareCommittedCommandWorkflow(t)
	_ = fixture

	tests := []struct {
		name   string
		cmd    *cobra.Command
		args   []string
		assert func(*testing.T, string)
	}{
		{
			name: "query",
			cmd:  newQueryCommand(),
			args: []string{smokeQueryText},
			assert: func(t *testing.T, output string) {
				t.Helper()
				var result knowlapi.RetrieveResult
				if err := json.Unmarshal([]byte(output), &result); err != nil {
					t.Fatalf("decode query output: %v", err)
				}
				if result.Query != smokeQueryText || len(result.Evidence) == 0 || result.Evidence[0].PageId != smokePageID {
					t.Fatalf("query result = %#v", result)
				}
			},
		},
		{
			name: "operation",
			cmd:  newOperationCommand(),
			args: []string{ingest.OperationId},
			assert: func(t *testing.T, output string) {
				t.Helper()
				var result knowlapi.OperationResult
				if err := json.Unmarshal([]byte(output), &result); err != nil {
					t.Fatalf("decode operation output: %v", err)
				}
				if result.Id != ingest.OperationId || result.Status != knowlapi.OperationResultStatusCompleted {
					t.Fatalf("operation result = %#v", result)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr, err := executeCLICommand(test.cmd, test.args, nil)
			if err != nil {
				t.Fatalf("%s Execute() error: %v", test.name, err)
			}
			if strings.TrimSpace(stderr) != "" {
				t.Fatalf("%s stderr = %q, want empty", test.name, stderr)
			}
			test.assert(t, stdout)
		})
	}
}

func TestOperationCommandPrintsStructuredNotFoundError(t *testing.T) {
	fixture := newCommandWorkflowFixture(t, true)
	withLocalWorkflowSessionFactory(t, fixture.newSessionFactory(t))

	stdout, stderr, err := executeCLICommand(newOperationCommand(), []string{"op_missing"}, nil)
	if err == nil {
		t.Fatal("operation Execute() error = nil, want workflow failure")
	}
	var workflowErr *workflowCommandError
	if !errors.As(err, &workflowErr) {
		t.Fatalf("operation Execute() error = %T, want workflowCommandError", err)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, `"error":"not_found"`) {
		t.Fatalf("stderr = %q, want structured not_found error", stderr)
	}
}

func TestIngestCommandPrintsStructuredWorkflowError(t *testing.T) {
	fixture := newCommandWorkflowFixture(t, true)
	withLocalWorkflowSessionFactory(t, fixture.newSessionFactory(t))

	inputPath := writeJSONFixture(t, knowlapi.IngestRequest{})

	stdout, stderr, err := executeCLICommand(newIngestCommand(), []string{workflowInputFlagUsage, inputPath}, nil)
	if err == nil {
		t.Fatal("ingest Execute() error = nil, want workflow failure")
	}
	var workflowErr *workflowCommandError
	if !errors.As(err, &workflowErr) {
		t.Fatalf("ingest Execute() error = %T, want workflowCommandError", err)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, `"error":"invalid_request"`) {
		t.Fatalf("stderr = %q, want structured invalid_request error", stderr)
	}
}

func TestLocalWorkflowRunnerPropagatesStartError(t *testing.T) {
	original := newLocalWorkflowSession
	t.Cleanup(func() { newLocalWorkflowSession = original })

	host := &stubLocalWorkflowHost{
		handler:  http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		startErr: errors.New("boom"),
	}
	newLocalWorkflowSession = func(context.Context) (localWorkflowSession, error) {
		return localWorkflowSession{
			Host:            host,
			ShutdownTimeout: time.Second,
		}, nil
	}

	_, err := newLocalWorkflowRunner().Execute(context.Background(), localWorkflowRequest{
		Method: http.MethodGet,
		Path:   "/v1/retrieve?query=test",
	})
	if err == nil || !strings.Contains(err.Error(), "start local workflow host") {
		t.Fatalf("Execute() error = %v, want start error", err)
	}
	if host.stopCalls != 0 {
		t.Fatalf("Stop() calls = %d, want 0 when Start fails", host.stopCalls)
	}
}

func TestLocalWorkflowRunnerClosesHostAfterStartError(t *testing.T) {
	original := newLocalWorkflowSession
	t.Cleanup(func() { newLocalWorkflowSession = original })

	host := &stubClosableLocalWorkflowHost{
		stubLocalWorkflowHost: stubLocalWorkflowHost{
			handler:  http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
			startErr: errors.New("boom"),
		},
	}
	newLocalWorkflowSession = func(context.Context) (localWorkflowSession, error) {
		return localWorkflowSession{
			Host:            host,
			ShutdownTimeout: time.Second,
		}, nil
	}

	_, err := newLocalWorkflowRunner().Execute(context.Background(), localWorkflowRequest{
		Method: http.MethodGet,
		Path:   "/v1/retrieve?query=test",
	})
	if err == nil || !strings.Contains(err.Error(), "start local workflow host") {
		t.Fatalf("Execute() error = %v, want start error", err)
	}
	if host.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1 after Start fails", host.closeCalls)
	}
	if host.stopCalls != 0 {
		t.Fatalf("Stop() calls = %d, want 0 when cleanup uses Close()", host.stopCalls)
	}
}

type stubLocalWorkflowHost struct {
	handler    http.Handler
	startErr   error
	stopErr    error
	startCalls int
	stopCalls  int
}

func (host *stubLocalWorkflowHost) Start(context.Context) error {
	host.startCalls++
	return host.startErr
}

func (host *stubLocalWorkflowHost) Stop(context.Context) error {
	host.stopCalls++
	return host.stopErr
}

func (host *stubLocalWorkflowHost) Handler() http.Handler {
	return host.handler
}

type stubClosableLocalWorkflowHost struct {
	stubLocalWorkflowHost
	closeErr   error
	closeCalls int
}

func (host *stubClosableLocalWorkflowHost) Close() error {
	host.closeCalls++
	return host.closeErr
}

func TestRootCommandLoadsConfigIntoRunContext(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)

	root := newRootCommand()
	root.SetArgs([]string{initCommandName})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute init: %v", err)
	}
	configPath := filepath.Join(workspace, ".config", appName, "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("generated config: %v", err)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	assertNoRetiredConfigKeys(t, content)
}

func TestLoadConfigDefaultsToSQLiteWithoutBackendSection(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)

	ctx, err := loadConfig(context.Background(), "", "")
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}
	storage, err := storageSettings(ctx)
	if err != nil {
		t.Fatalf("normalize default storage: %v", err)
	}
	wantPath := filepath.Join(workingDir, ".knowl", "knowl.sqlite")
	if storage.Driver != knowl.StoreSQLite || storage.Path != wantPath {
		t.Fatalf("default storage = %#v, want sqlite path %q", storage, wantPath)
	}
}

func TestCheckedInConfigArtifactUsesTypedBaldaCompatibleShape(t *testing.T) {
	repoRoot := testRepoRoot(t)
	t.Chdir(repoRoot)
	clearKnowlEnv(t)

	configPath := filepath.Join(repoRoot, ".config", appName, "config.yaml")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read checked-in config: %v", err)
	}
	assertNoRetiredConfigKeys(t, content)

	ctx, err := loadConfig(context.Background(), "", "")
	if err != nil {
		t.Fatalf("load checked-in config: %v", err)
	}
	loaded, err := configFromContext(ctx)
	if err != nil {
		t.Fatalf("read loaded config: %v", err)
	}
	if _, ok := loaded.Document.Runtime.Providers[loaded.Document.Knowl.Provider]; !ok {
		t.Fatalf("knowl.provider %q not present in runtime.providers", loaded.Document.Knowl.Provider)
	}
	storage, err := storageSettings(ctx)
	if err != nil {
		t.Fatalf("normalize checked-in storage: %v", err)
	}
	wantPath := filepath.Join(repoRoot, "knowledge", ".knowl", "knowl.sqlite")
	if storage.Driver != knowl.StoreSQLite || storage.Path != wantPath {
		t.Fatalf("checked-in storage = %#v, want sqlite path %q", storage, wantPath)
	}
	if token := strings.TrimSpace(loaded.Document.Knowl.Operator.Token); token != "" && !strings.HasPrefix(token, "replace-") {
		t.Fatalf("operator token = %q, want empty or placeholder", token)
	}
}

func TestEmbeddedDefaultConfigArtifactLoadsThroughProductionTypes(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	clearKnowlEnv(t)
	assertNoRetiredConfigKeys(t, defaultKnowlConfig)

	ctx, err := loadConfig(context.Background(), "", "")
	if err != nil {
		t.Fatalf("load embedded config: %v", err)
	}
	loaded, err := configFromContext(ctx)
	if err != nil {
		t.Fatalf("read loaded config: %v", err)
	}
	if _, ok := loaded.Document.Runtime.Providers[loaded.Document.Knowl.Provider]; !ok {
		t.Fatalf("embedded knowl.provider %q not present in runtime.providers", loaded.Document.Knowl.Provider)
	}
	storage, err := storageSettings(ctx)
	if err != nil {
		t.Fatalf("normalize embedded storage: %v", err)
	}
	wantPath := filepath.Join(workingDir, ".knowl", "knowl.sqlite")
	if storage.Driver != knowl.StoreSQLite || storage.Path != wantPath {
		t.Fatalf("embedded storage = %#v, want sqlite path %q", storage, wantPath)
	}
	if token := strings.TrimSpace(loaded.Document.Knowl.Operator.Token); token != "" && !strings.HasPrefix(token, "replace-") {
		t.Fatalf("embedded operator token = %q, want empty or placeholder", token)
	}
}

func TestOperatorDocsUseCanonicalIngestConfigShape(t *testing.T) {
	t.Parallel()

	repoRoot := testRepoRoot(t)
	tests := []struct {
		name string
		path string
		want []string
	}{
		{
			name: "readme",
			path: filepath.Join(repoRoot, "README.md"),
			want: []string{"knowl.ingest.auto_apply", "auto_apply: false"},
		},
		{
			name: "operations",
			path: filepath.Join(repoRoot, "docs", "operations.md"),
			want: []string{"KNOWL_INGEST_AUTO_APPLY", "knowl.ingest.auto_apply"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatalf("read %s: %v", test.path, err)
			}
			text := string(content)
			for _, unwanted := range []string{"maintenance:", "maintenance.review", "maintenance.auto_apply", "KNOWL_MAINTENANCE_"} {
				if strings.Contains(text, unwanted) {
					t.Fatalf("%s contains retired ingest-policy reference %q", test.path, unwanted)
				}
			}
			for _, want := range test.want {
				if !strings.Contains(text, want) {
					t.Fatalf("%s missing canonical ingest-policy reference %q", test.path, want)
				}
			}
		})
	}
}

func TestLoadConfigMatchesBaldaRuntimeDocumentAndOverrides(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	if err := os.MkdirAll(filepath.Join(workingDir, ".config", appName), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	config := `runtime:
  providers:
    codex:
      type: codex_acp
      codex_acp:
        model: gpt-5-codex
profiles:
  fast:
    knowl:
      provider: codex
      workspace:
        path: profile-workspace
knowl:
  provider: codex
  workspace:
    path: file-workspace
  storage:
    type: sqlite
    sqlite:
      path: .knowl/state.db
`
	if err := os.WriteFile(filepath.Join(workingDir, ".config", appName, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("KNOWL_WORKSPACE_PATH", "env-workspace")

	ctx, err := loadConfig(context.Background(), "", "fast")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	loaded, err := configFromContext(ctx)
	if err != nil {
		t.Fatalf("read loaded config: %v", err)
	}
	if loaded.Profile != "fast" {
		t.Fatalf("profile = %q, want fast", loaded.Profile)
	}
	provider, ok := loaded.Document.Runtime.Providers["codex"]
	if !ok || provider.CodexACP == nil || provider.CodexACP.Model != "gpt-5-codex" {
		t.Fatalf("runtime provider = %#v, want Balda-compatible codex provider", provider)
	}
	if loaded.Document.Knowl.Provider != "codex" {
		t.Fatalf("knowl.provider = %q, want codex", loaded.Document.Knowl.Provider)
	}
	if loaded.Document.Knowl.Workspace.Path != "env-workspace" {
		t.Fatalf("workspace.path = %q, want env-workspace", loaded.Document.Knowl.Workspace.Path)
	}
	if loaded.Document.Knowl.Storage.SQLite == nil || loaded.Document.Knowl.Storage.SQLite.Path != ".knowl/state.db" {
		t.Fatalf("sqlite config = %#v, want decoded typed section", loaded.Document.Knowl.Storage.SQLite)
	}
}

func TestLoadConfigUsesConfigDirLayout(t *testing.T) {
	workingDir := t.TempDir()
	configDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configDir, appName), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	config := `runtime:
  providers:
    hosted:
      type: openai
      openai:
        model: gpt-5-mini
knowl:
  provider: hosted
  storage:
    type: postgres
    postgres:
      dsn: postgres://user:secret@localhost/knowl
`
	if err := os.WriteFile(filepath.Join(configDir, appName, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(workingDir); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	ctx, err := loadConfig(context.Background(), configDir, "")
	if err != nil {
		t.Fatalf("load config from config-dir: %v", err)
	}
	loaded, err := configFromContext(ctx)
	if err != nil {
		t.Fatalf("read loaded config: %v", err)
	}
	if loaded.Document.Knowl.Provider != "hosted" {
		t.Fatalf("knowl.provider = %q, want hosted", loaded.Document.Knowl.Provider)
	}
	if _, err := storageSettings(ctx); err != nil {
		t.Fatalf("normalize postgres storage: %v", err)
	}
	if got := configOutputPath(loaded); !strings.HasPrefix(got, configDir) {
		t.Fatalf("config output path = %q, want under config-dir %q", got, configDir)
	}
}

func TestLoadConfigRejectsInvalidRuntimeProvider(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	if err := os.MkdirAll(filepath.Join(workingDir, ".config", appName), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	config := `runtime:
  providers:
    broken:
      type: codex_acp
      opencode_acp: {}
knowl:
  provider: broken
`
	if err := os.WriteFile(filepath.Join(workingDir, ".config", appName, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := loadConfig(context.Background(), "", "")
	if err == nil {
		t.Fatal("loadConfig() error = nil, want invalid runtime provider error")
	}
	if !strings.Contains(err.Error(), "runtime config") {
		t.Fatalf("loadConfig() error = %q, want shared runtime validation context", err)
	}
}

func TestSelectedRuntimeProviderValidatesSelectorBeforeHostConstruction(t *testing.T) {
	providerConfig := agentconfig.Config{
		Type:   "openai",
		OpenAI: &agentconfig.LocalAPIConfig{Model: "test-model"},
	}
	for _, test := range []struct {
		name       string
		providerID string
		wantError  string
	}{
		{name: "empty", wantError: "knowl.provider is required"},
		{name: "unknown", providerID: "missing", wantError: "validate knowl.provider"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), loadedConfigContextKey{}, loadedConfig{
				Document: knowlConfigDocument{
					Runtime: appconfig.RuntimeConfig{Providers: map[string]agentconfig.Config{knownProviderID: providerConfig}},
					Knowl:   AppConfig{Provider: test.providerID},
				},
			})
			if _, _, err := selectedRuntimeProvider(ctx); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("selectedRuntimeProvider() error = %v, want %q", err, test.wantError)
			}
		})
	}

	ctx := context.WithValue(context.Background(), loadedConfigContextKey{}, loadedConfig{
		Document: knowlConfigDocument{
			Runtime: appconfig.RuntimeConfig{Providers: map[string]agentconfig.Config{knownProviderID: providerConfig}},
			Knowl:   AppConfig{Provider: knownProviderID},
		},
	})
	factory, providerID, err := selectedRuntimeProvider(ctx)
	if err != nil {
		t.Fatalf("selectedRuntimeProvider() error: %v", err)
	}
	if factory == nil || providerID != knownProviderID {
		t.Fatalf("selectedRuntimeProvider() = (%#v, %q), want factory and %s", factory, providerID, knownProviderID)
	}
}

func testRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve commands_test.go path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func clearKnowlEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"KNOWL_PROVIDER",
		"KNOWL_WORKSPACE_PATH",
		"KNOWL_STORAGE_TYPE",
		"KNOWL_STORAGE_SQLITE_PATH",
		"KNOWL_STORAGE_POSTGRES_DSN",
		"KNOWL_INGEST_AUTO_APPLY",
		"KNOWL_SCOPE",
		"KNOWL_SERVER_LISTEN_ADDR",
		"KNOWL_OPERATOR_TOKEN",
		"KNOWL_MAINTENANCE_REVIEW",
		"KNOWL_MAINTENANCE_AUTO_APPLY",
	} {
		t.Setenv(key, "")
	}
}

func assertNoRetiredConfigKeys(t *testing.T, content []byte) {
	t.Helper()
	var document map[string]any
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("decode config artifact: %v", err)
	}
	for _, key := range []string{"workspace", "scope", "store"} {
		if _, exists := document[key]; exists {
			t.Fatalf("config artifact contains retired top-level key %q", key)
		}
	}
	knowlSection, ok := document["knowl"].(map[string]any)
	if !ok {
		t.Fatal("config artifact is missing knowl section")
	}
	for _, key := range []string{"maintenance"} {
		if _, exists := knowlSection[key]; exists {
			t.Fatalf("config artifact contains retired knowl key %q", key)
		}
	}
	storageSection, ok := knowlSection["storage"].(map[string]any)
	if !ok {
		return
	}
	for _, key := range []string{"driver", "path"} {
		if _, exists := storageSection[key]; exists {
			t.Fatalf("config artifact contains retired knowl.storage key %q", key)
		}
	}
}

func commandHelpOutput(t *testing.T, cmd *cobra.Command) string {
	t.Helper()

	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{commandHelpFlag})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute help for %s: %v", cmd.Name(), err)
	}
	return output.String()
}

type commandWorkflowFixture struct {
	config     knowl.Config
	schema     domain.SchemaDocument
	maintainer provider.Fixture
}

func newCommandWorkflowFixture(t *testing.T, autoApply bool) commandWorkflowFixture {
	t.Helper()

	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	schema, err := workspace.Schema(context.Background(), "local")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.ListenAddr = loopbackListenAddr
	config.OperatorToken = ""
	config.IngestOptions.AutoApply = autoApply
	return commandWorkflowFixture{
		config: config,
		schema: schema,
		maintainer: provider.Fixture{Result: domain.ModelEditPlan{
			SchemaDigest: schema.Digest,
			SourceRefs:   []string{smokeSourceRef},
			Edits:        []domain.FileEdit{{Path: smokePagePath, Content: []byte(smokePageContent)}},
		}},
	}
}

func (fixture commandWorkflowFixture) newSessionFactory(t *testing.T) localWorkflowSessionFactory {
	t.Helper()

	return func(ctx context.Context) (localWorkflowSession, error) {
		host, err := knowl.NewHost(ctx, fixture.config, fixture.maintainer)
		if err != nil {
			return localWorkflowSession{}, err
		}
		return localWorkflowSession{
			Host:            host,
			ShutdownTimeout: fixture.config.ShutdownTimeout,
		}, nil
	}
}

func (fixture commandWorkflowFixture) pagePath(relative string) string {
	return filepath.Join(fixture.config.Workspace, filepath.FromSlash(relative))
}

func withLocalWorkflowSessionFactory(t *testing.T, factory localWorkflowSessionFactory) {
	t.Helper()

	original := newLocalWorkflowSession
	newLocalWorkflowSession = factory
	t.Cleanup(func() { newLocalWorkflowSession = original })
}

func executeCLICommand(cmd *cobra.Command, args []string, stdin []byte) (string, string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetArgs(args)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if stdin == nil {
		cmd.SetIn(bytes.NewReader(nil))
	} else {
		cmd.SetIn(bytes.NewReader(stdin))
	}
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func writeJSONFixture(t *testing.T, value any) string {
	t.Helper()

	content, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write JSON fixture: %v", err)
	}
	return path
}

func pointerTo(value string) *string {
	return &value
}

func prepareCommittedCommandWorkflow(t *testing.T) (commandWorkflowFixture, knowlapi.IngestResult) {
	t.Helper()

	fixture := newCommandWorkflowFixture(t, true)
	withLocalWorkflowSessionFactory(t, fixture.newSessionFactory(t))
	inputPath := writeJSONFixture(t, knowlapi.IngestRequest{
		Content:        pointerTo(smokeSourceText),
		Origin:         pointerTo(smokeSourceID),
		IdempotencyKey: pointerTo(smokeSourceVersion),
	})
	stdout, stderr, err := executeCLICommand(newIngestCommand(), []string{workflowInputFlagUsage, inputPath}, nil)
	if err != nil {
		t.Fatalf("prepare committed ingest Execute() error: %v", err)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("prepare committed ingest stderr = %q, want empty", stderr)
	}
	var result knowlapi.IngestResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode committed ingest output: %v", err)
	}
	if result.Status != knowlapi.IngestResultStatusCompleted {
		t.Fatalf("prepare committed ingest status = %q, want %q", result.Status, knowlapi.IngestResultStatusCompleted)
	}
	return fixture, result
}
