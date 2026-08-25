package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/pkg/knowl"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/spf13/cobra"
)

const (
	knownProviderID           = "known"
	commandHelpFlag           = "--help"
	commandOperationsSourceID = "operations"
	sourceSyncAllFlag         = "--all"
	retrieveSourceFlag        = "--source"
	designDocRelativePath     = "docs/design.md"
	sourceNamespacePattern    = "wiki/sources/<source_id>/**"
	operatorTokenEnvName      = "KNOWL_OPERATOR_TOKEN"
	mcpRetrieveToolName       = "knowl_retrieve"
	mcpIngestToolName         = "knowl_ingest"
	mcpOperationToolName      = "knowl_operation"
)

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
		"KNOWL_SCOPE",
		"KNOWL_SERVER_LISTEN_ADDR",
		operatorTokenEnvName,
	} {
		t.Setenv(key, "")
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
