package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"testing"
	"time"

	knowlruntime "github.com/baldaworks/knowl/pkg/knowl"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	knowlmcp "github.com/baldaworks/knowl/pkg/knowl/mcp"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

const (
	acceptanceTimeout         = 15 * time.Second
	acceptanceBuildTimeout    = 2 * time.Minute
	fixturePlanEnv            = "KNOWL_TEST_ACP_PLAN"
	fixtureGateDirEnv         = "KNOWL_TEST_ACP_GATE_DIR"
	acceptanceQueryArgument   = "query"
	acceptanceStatusQueued    = "queued"
	acceptanceStatusCompleted = "completed"
	acceptanceStatusFailed    = "failed"
)

type acceptanceProject struct {
	root      string
	database  string
	listen    string
	workspace *contentfs.Workspace
	schema    domain.SchemaDocument
}

type acceptanceConfig struct {
	Runtime struct {
		Providers map[string]acceptanceProvider `yaml:"providers"`
	} `yaml:"runtime"`
	Knowl struct {
		Provider  string `yaml:"provider"`
		Workspace struct {
			Path string `yaml:"path"`
		} `yaml:"workspace"`
		Storage struct {
			Type   string `yaml:"type"`
			SQLite struct {
				Path string `yaml:"path"`
			} `yaml:"sqlite"`
		} `yaml:"storage"`
		Server struct {
			ListenAddr string `yaml:"listen_addr"`
		} `yaml:"server"`
	} `yaml:"knowl"`
}

type acceptanceProvider struct {
	Type       string               `yaml:"type"`
	GenericACP acceptanceGenericACP `yaml:"generic_acp"`
}

type acceptanceGenericACP struct {
	Command []string `yaml:"cmd"`
}

type acceptancePlan struct {
	SchemaDigest string               `json:"schema_digest"`
	SourceRefs   []string             `json:"source_refs"`
	Edits        []acceptancePlanEdit `json:"edits"`
}

type acceptancePlanEdit struct {
	Path           string `json:"path"`
	ExpectedDigest string `json:"expected_digest,omitempty"`
	Content        string `json:"content"`
}

type acceptanceOperationState struct {
	Status         string
	WorkAttempt    int
	WorkLeaseToken string
}

type capturedReadCloser struct {
	io.Reader
	io.Closer
}

type mcpAcceptanceProcess struct {
	command *exec.Cmd
	session *sdkmcp.ClientSession
	stdout  bytes.Buffer
	stderr  bytes.Buffer
	waitMu  sync.Mutex
	waited  bool
}

func TestMCPStdioRealBinaryAcceptance(t *testing.T) {
	clearKnowlEnv(t)
	binaryDir := t.TempDir()
	knowlBinary := buildAcceptanceBinary(t, filepath.Join(binaryDir, "knowl"), "./cmd/knowl")
	fixtureBinary := buildAcceptanceBinary(t, filepath.Join(binaryDir, "acpfixture"), "./cmd/knowl/testdata/acpfixture")

	t.Run("protocol lifecycle", func(t *testing.T) {
		project := newAcceptanceProject(t, fixtureBinary)
		process := startMCPAcceptanceProcess(t, knowlBinary, project, nil)
		assertInitializedServer(t, process.session)
		assertExactMCPTools(t, process.session)
		assertNoTCPListener(t, project.listen)

		result := callAcceptanceTool[knowlmcp.RetrieveResult](t, process.session, mcpRetrieveToolName, map[string]any{acceptanceQueryArgument: "absent"})
		if result.Query != "absent" || len(result.Evidence) != 0 {
			t.Fatalf("retrieve result = %#v", result)
		}
		process.closeEOF(t)
		process.assertStdoutFrames(t)
		if process.stderr.Len() != 0 {
			t.Fatalf("stdio stderr = %q, want empty", process.stderr.Bytes())
		}
	})

	t.Run("signals", func(t *testing.T) {
		for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
			t.Run(signal.String(), func(t *testing.T) {
				project := newAcceptanceProject(t, fixtureBinary)
				process := startMCPAcceptanceProcess(t, knowlBinary, project, nil)
				assertInitializedServer(t, process.session)
				if err := process.command.Process.Signal(signal); err != nil {
					t.Fatalf("signal MCP process: %v", err)
				}
				process.wait(t, true)
				process.assertStdoutFrames(t)
			})
		}
	})

	t.Run("project cwd isolation", func(t *testing.T) {
		first := newAcceptanceProject(t, fixtureBinary)
		second := newAcceptanceProject(t, fixtureBinary)
		firstEnv := []string{fixturePlanEnv + "=" + encodedAcceptancePlan(t, first, "alpha", "1", "entities/alpha", "Alpha Beacon")}
		secondEnv := []string{fixturePlanEnv + "=" + encodedAcceptancePlan(t, second, "beta", "1", "entities/beta", "Beta Beacon")}
		firstProcess := startMCPAcceptanceProcess(t, knowlBinary, first, firstEnv)
		secondProcess := startMCPAcceptanceProcess(t, knowlBinary, second, secondEnv)

		firstIngest := callAcceptanceIngest(t, firstProcess.session, "alpha", "1")
		secondIngest := callAcceptanceIngest(t, secondProcess.session, "beta", "1")
		waitAcceptanceOperation(t, firstProcess.session, firstIngest.OperationID)
		waitAcceptanceOperation(t, secondProcess.session, secondIngest.OperationID)

		firstResult := callAcceptanceTool[knowlmcp.RetrieveResult](t, firstProcess.session, mcpRetrieveToolName, map[string]any{acceptanceQueryArgument: "Alpha Beacon"})
		secondResult := callAcceptanceTool[knowlmcp.RetrieveResult](t, secondProcess.session, mcpRetrieveToolName, map[string]any{acceptanceQueryArgument: "Beta Beacon"})
		assertOnlyEvidencePage(t, firstResult, "entities/alpha")
		assertOnlyEvidencePage(t, secondResult, "entities/beta")
		crossFirst := callAcceptanceTool[knowlmcp.RetrieveResult](t, firstProcess.session, mcpRetrieveToolName, map[string]any{acceptanceQueryArgument: "Beta Beacon"})
		crossSecond := callAcceptanceTool[knowlmcp.RetrieveResult](t, secondProcess.session, mcpRetrieveToolName, map[string]any{acceptanceQueryArgument: "Alpha Beacon"})
		assertNoEvidencePage(t, crossFirst, "entities/beta")
		assertNoEvidencePage(t, crossSecond, "entities/alpha")
		firstProcess.closeEOF(t)
		secondProcess.closeEOF(t)
		firstProcess.assertStdoutFrames(t)
		secondProcess.assertStdoutFrames(t)
	})

	t.Run("shared project concurrency", func(t *testing.T) {
		project := newAcceptanceProject(t, fixtureBinary)
		gate := t.TempDir()
		environment := []string{
			fixturePlanEnv + "=" + encodedAcceptancePlan(t, project, "shared", "1", "entities/shared", "Shared Beacon"),
			fixtureGateDirEnv + "=" + gate,
		}
		firstProcess := startMCPAcceptanceProcess(t, knowlBinary, project, environment)
		firstIngest := callAcceptanceIngest(t, firstProcess.session, "shared", "1")
		waitForPath(t, filepath.Join(gate, "entered"))

		secondProcess := startMCPAcceptanceProcess(t, knowlBinary, project, environment)
		active := readAcceptanceOperationState(t, project.database, firstIngest.OperationID)
		if active.WorkAttempt != 1 || active.WorkLeaseToken == "" {
			t.Fatalf("active operation state = %#v", active)
		}

		type concurrentResult struct {
			run    knowlruntime.RunOnceResult
			replay knowlmcp.IngestResult
			err    error
		}
		results := make(chan concurrentResult, 2)
		go func() {
			run, err := runAcceptanceCycle(knowlBinary, project, environment)
			results <- concurrentResult{run: run, err: err}
		}()
		go func() {
			replay := callAcceptanceToolNoFail[knowlmcp.IngestResult](secondProcess.session, mcpIngestToolName, map[string]any{
				testContentArgument: "fixture source", testOriginArgument: "shared", testIdempotencyArgument: "1",
			})
			results <- concurrentResult{replay: replay.value, err: replay.err}
		}()
		for range 2 {
			result := <-results
			if result.err != nil {
				t.Fatalf("concurrent command: %v", result.err)
			}
			if result.replay.OperationID != "" && result.replay.OperationID != firstIngest.OperationID {
				t.Fatalf("replayed operation = %#v, want %q", result.replay, firstIngest.OperationID)
			}
			if result.run.Operations.Total != 0 {
				t.Fatalf("concurrent run claimed leased work: %#v", result.run.Operations)
			}
		}

		releaseGate(t, gate)
		waitAcceptanceOperation(t, firstProcess.session, firstIngest.OperationID)
		waitAcceptanceOperation(t, secondProcess.session, firstIngest.OperationID)
		assertOperationCount(t, project.database, 1)
		assertInvocationCount(t, gate, 1)
		assertCommittedWorkspace(t, project, map[domain.PageID]string{
			"entities/shared": "Shared Beacon",
		})
		runAcceptanceValidate(t, knowlBinary, project)
		firstProcess.closeEOF(t)
		secondProcess.closeEOF(t)
	})

	t.Run("durable recovery", func(t *testing.T) {
		project := newAcceptanceProject(t, fixtureBinary)
		gate := t.TempDir()
		environment := []string{
			fixturePlanEnv + "=" + encodedAcceptancePlan(t, project, "recovery", "1", "entities/recovery", "Recovery Beacon"),
			fixtureGateDirEnv + "=" + gate,
		}
		firstProcess := startMCPAcceptanceProcess(t, knowlBinary, project, environment)
		ingested := callAcceptanceIngest(t, firstProcess.session, "recovery", "1")
		waitForPath(t, filepath.Join(gate, "entered"))
		before := readAcceptanceOperationState(t, project.database, ingested.OperationID)
		if before.WorkAttempt != 1 || before.WorkLeaseToken == "" {
			t.Fatalf("operation before crash = %#v", before)
		}
		if err := firstProcess.command.Process.Kill(); err != nil {
			t.Fatalf("kill first MCP process: %v", err)
		}
		releaseGate(t, gate)
		firstProcess.wait(t, false)
		expireAcceptanceLease(t, project.database, ingested.OperationID)

		secondProcess := startMCPAcceptanceProcess(t, knowlBinary, project, environment)
		waitAcceptanceOperation(t, secondProcess.session, ingested.OperationID)
		after := readAcceptanceOperationState(t, project.database, ingested.OperationID)
		if after.Status != string(domain.StatusCommitted) || after.WorkAttempt != 2 || after.WorkLeaseToken != "" {
			t.Fatalf("operation after recovery = %#v", after)
		}
		assertOperationCount(t, project.database, 1)
		assertInvocationCount(t, gate, 2)
		assertCommittedWorkspace(t, project, map[domain.PageID]string{"entities/recovery": "Recovery Beacon"})
		secondProcess.closeEOF(t)
	})
}

func buildAcceptanceBinary(t *testing.T, output, packagePath string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), acceptanceBuildTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-o", output, packagePath)
	command.Dir = testRepoRoot(t)
	combined, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", packagePath, err, combined)
	}
	return output
}

func newAcceptanceProject(t *testing.T, fixtureBinary string) acceptanceProject {
	t.Helper()
	root := t.TempDir()
	workspace, err := contentfs.New(root)
	if err != nil {
		t.Fatalf("create acceptance workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("initialize acceptance workspace: %v", err)
	}
	schema, err := workspace.Schema(context.Background(), knowlruntime.DefaultScope)
	if err != nil {
		t.Fatalf("read acceptance schema: %v", err)
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve acceptance address: %v", err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("release acceptance address: %v", err)
	}

	var config acceptanceConfig
	config.Runtime.Providers = map[string]acceptanceProvider{
		"fixture": {Type: "generic_acp", GenericACP: acceptanceGenericACP{Command: []string{fixtureBinary}}},
	}
	config.Knowl.Provider = "fixture"
	config.Knowl.Workspace.Path = "."
	config.Knowl.Storage.Type = "sqlite"
	config.Knowl.Storage.SQLite.Path = ".knowl/state.db"
	config.Knowl.Server.ListenAddr = address
	encoded, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("encode acceptance config: %v", err)
	}
	configDir := filepath.Join(root, ".config", appName)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create acceptance config directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), encoded, 0o600); err != nil {
		t.Fatalf("write acceptance config: %v", err)
	}
	return acceptanceProject{
		root: root, database: filepath.Join(root, ".knowl", "state.db"), listen: address,
		workspace: workspace, schema: schema,
	}
}

func encodedAcceptancePlan(t *testing.T, project acceptanceProject, origin, version, pageID, title string) string {
	t.Helper()
	sourceRef := "inline:" + origin + "@" + version
	rootContent, err := os.ReadFile(filepath.Join(project.root, "wiki", "index.md"))
	if err != nil {
		t.Fatalf("read acceptance root catalog: %v", err)
	}
	rootDigest := sha256.Sum256(rootContent)
	rootAfter := string(bytes.TrimRight(rootContent, "\n")) + "\n\n* [" + title + "](" + pageID + ".md)\n"
	plan := acceptancePlan{
		SchemaDigest: project.schema.Digest,
		SourceRefs:   []string{sourceRef},
		Edits: []acceptancePlanEdit{
			{
				Path:    "wiki/" + pageID + ".md",
				Content: fmt.Sprintf("---\nid: %s\ntitle: %s\ntype: entity\nsource_refs:\n  - %s\n---\n# %s\n", pageID, title, sourceRef, title),
			},
			{Path: indexFile, ExpectedDigest: hex.EncodeToString(rootDigest[:]), Content: rootAfter},
		},
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("encode acceptance plan: %v", err)
	}
	return string(encoded)
}

func startMCPAcceptanceProcess(t *testing.T, binary string, project acceptanceProject, extraEnv []string) *mcpAcceptanceProcess {
	t.Helper()
	process := &mcpAcceptanceProcess{}
	process.command = exec.Command(binary, mcpCommandName, mcpStdioCommandName)
	process.command.Dir = project.root
	process.command.Env = append(os.Environ(), extraEnv...)
	stdout, err := process.command.StdoutPipe()
	if err != nil {
		t.Fatalf("open MCP stdout: %v", err)
	}
	stdin, err := process.command.StdinPipe()
	if err != nil {
		t.Fatalf("open MCP stdin: %v", err)
	}
	process.command.Stderr = &process.stderr
	if err := process.command.Start(); err != nil {
		t.Fatalf("start MCP process: %v", err)
	}
	t.Cleanup(func() {
		process.waitMu.Lock()
		waited := process.waited
		process.waitMu.Unlock()
		if !waited && process.command.Process != nil {
			_ = process.command.Process.Kill()
			process.wait(t, false)
		}
	})
	transport := &sdkmcp.IOTransport{
		Reader: capturedReadCloser{Reader: io.TeeReader(stdout, &process.stdout), Closer: stdout},
		Writer: stdin,
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "knowl-acceptance", Version: "1.0.0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), acceptanceTimeout)
	defer cancel()
	process.session, err = client.Connect(ctx, transport, nil)
	if err != nil {
		_ = process.command.Process.Kill()
		process.wait(t, false)
		t.Fatalf("initialize MCP process: %v; stderr=%s", err, process.stderr.Bytes())
	}
	return process
}

func (process *mcpAcceptanceProcess) closeEOF(t *testing.T) {
	t.Helper()
	if err := process.session.Close(); err != nil {
		t.Fatalf("close MCP session: %v; stderr=%s", err, process.stderr.Bytes())
	}
	process.wait(t, true)
}

func (process *mcpAcceptanceProcess) wait(t *testing.T, wantSuccess bool) {
	t.Helper()
	process.waitMu.Lock()
	if process.waited {
		process.waitMu.Unlock()
		return
	}
	process.waited = true
	process.waitMu.Unlock()
	done := make(chan error, 1)
	go func() { done <- process.command.Wait() }()
	select {
	case err := <-done:
		if wantSuccess && err != nil {
			t.Fatalf("MCP process exit: %v; stderr=%s", err, process.stderr.Bytes())
		}
		if !wantSuccess && err == nil {
			t.Fatal("MCP process exited successfully, want forced termination")
		}
	case <-time.After(acceptanceTimeout):
		_ = process.command.Process.Kill()
		t.Fatal("MCP process did not exit before deadline")
	}
}

func (process *mcpAcceptanceProcess) assertStdoutFrames(t *testing.T) {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(process.stdout.Bytes()))
	frames := 0
	for scanner.Scan() {
		var frame struct {
			JSONRPC string `json:"jsonrpc"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			t.Fatalf("stdout frame %d is not JSON-RPC: %v", frames, err)
		}
		if frame.JSONRPC != "2.0" {
			t.Fatalf("stdout frame %d JSON-RPC version = %q", frames, frame.JSONRPC)
		}
		frames++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan stdout frames: %v", err)
	}
	if frames == 0 {
		t.Fatal("MCP process emitted no stdout frames")
	}
}

func assertInitializedServer(t *testing.T, session *sdkmcp.ClientSession) {
	t.Helper()
	initialized := session.InitializeResult()
	if initialized == nil || initialized.ServerInfo == nil || initialized.ServerInfo.Name != appName || initialized.ProtocolVersion == "" {
		t.Fatalf("initialize result = %#v", initialized)
	}
}

func assertExactMCPTools(t *testing.T, session *sdkmcp.ClientSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), acceptanceTimeout)
	defer cancel()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list MCP tools: %v", err)
	}
	type toolContract struct {
		Name     string
		ReadOnly bool
	}
	got := make([]toolContract, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		if tool.Annotations == nil {
			t.Fatalf("tool %q has no annotations", tool.Name)
		}
		got = append(got, toolContract{Name: tool.Name, ReadOnly: tool.Annotations.ReadOnlyHint})
	}
	want := []toolContract{
		{Name: mcpIngestToolName, ReadOnly: false},
		{Name: mcpOperationToolName, ReadOnly: true},
		{Name: mcpRetrieveToolName, ReadOnly: true},
	}
	sort.Slice(got, func(left, right int) bool { return got[left].Name < got[right].Name })
	sort.Slice(want, func(left, right int) bool { return want[left].Name < want[right].Name })
	if len(got) != len(want) {
		t.Fatalf("MCP tools = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("MCP tools = %#v, want %#v", got, want)
		}
	}
}

func assertNoTCPListener(t *testing.T, address string) {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("stdio process bound configured HTTP address: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener probe: %v", err)
	}
}

func callAcceptanceTool[T any](t *testing.T, session *sdkmcp.ClientSession, name string, arguments map[string]any) T {
	t.Helper()
	result := callAcceptanceToolNoFail[T](session, name, arguments)
	if result.err != nil {
		t.Fatalf("call MCP tool %q: %v", name, result.err)
	}
	return result.value
}

type acceptanceToolResult[T any] struct {
	value T
	err   error
}

func callAcceptanceToolNoFail[T any](session *sdkmcp.ClientSession, name string, arguments map[string]any) acceptanceToolResult[T] {
	ctx, cancel := context.WithTimeout(context.Background(), acceptanceTimeout)
	defer cancel()
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return acceptanceToolResult[T]{err: err}
	}
	if result.IsError {
		return acceptanceToolResult[T]{err: fmt.Errorf("tool returned structured failure: %#v", result.StructuredContent)}
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return acceptanceToolResult[T]{err: fmt.Errorf("encode structured tool result: %w", err)}
	}
	var decoded T
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return acceptanceToolResult[T]{err: fmt.Errorf("decode structured tool result: %w", err)}
	}
	return acceptanceToolResult[T]{value: decoded}
}

func callAcceptanceIngest(t *testing.T, session *sdkmcp.ClientSession, origin, idempotencyKey string) knowlmcp.IngestResult {
	t.Helper()
	result := callAcceptanceTool[knowlmcp.IngestResult](t, session, mcpIngestToolName, map[string]any{
		testContentArgument: "fixture source", testOriginArgument: origin, testIdempotencyArgument: idempotencyKey,
	})
	if result.OperationID == "" || (result.Status != acceptanceStatusQueued && result.Status != acceptanceStatusCompleted) {
		t.Fatalf("ingest result = %#v", result)
	}
	return result
}

func waitAcceptanceOperation(t *testing.T, session *sdkmcp.ClientSession, id domain.OperationID) knowlmcp.OperationResult {
	t.Helper()
	deadline := time.NewTimer(acceptanceTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		result := callAcceptanceTool[knowlmcp.OperationResult](t, session, mcpOperationToolName, map[string]any{"id": id})
		switch result.Status {
		case acceptanceStatusCompleted:
			return result
		case acceptanceStatusFailed:
			t.Fatalf("operation %q failed: %#v", id, result.Failure)
		}
		select {
		case <-deadline.C:
			t.Fatalf("operation %q did not complete", id)
		case <-ticker.C:
		}
	}
}

func assertOnlyEvidencePage(t *testing.T, result knowlmcp.RetrieveResult, pageID domain.PageID) {
	t.Helper()
	if len(result.Evidence) != 1 || result.Evidence[0].PageID != pageID {
		t.Fatalf("evidence = %#v, want page %q", result.Evidence, pageID)
	}
}

func assertNoEvidencePage(t *testing.T, result knowlmcp.RetrieveResult, pageID domain.PageID) {
	t.Helper()
	for _, evidence := range result.Evidence {
		if evidence.PageID == pageID {
			t.Fatalf("retrieve returned foreign page %q: %#v", pageID, result.Evidence)
		}
	}
}

func runAcceptanceCycle(binary string, project acceptanceProject, environment []string) (knowlruntime.RunOnceResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), acceptanceTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, binary, runCommandName, "--no-sync", "--no-hierarchy")
	command.Dir = project.root
	command.Env = append(os.Environ(), environment...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return knowlruntime.RunOnceResult{}, fmt.Errorf("run cycle: %w; stderr=%s", err, stderr.Bytes())
	}
	var result knowlruntime.RunOnceResult
	if err := json.Unmarshal(output, &result); err != nil {
		return knowlruntime.RunOnceResult{}, fmt.Errorf("decode run cycle: %w", err)
	}
	return result, nil
}

func runAcceptanceValidate(t *testing.T, binary string, project acceptanceProject) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), acceptanceTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, binary, validateCommandName)
	command.Dir = project.root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("validate accepted workspace: %v\n%s", err, output)
	}
}

func waitForPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.NewTimer(acceptanceTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect readiness path: %v", err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("readiness path %q was not created", path)
		case <-ticker.C:
		}
	}
}

func releaseGate(t *testing.T, directory string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, "release"), nil, 0o600); err != nil {
		t.Fatalf("release fixture gate: %v", err)
	}
}

func readAcceptanceOperationState(t *testing.T, database string, id domain.OperationID) acceptanceOperationState {
	t.Helper()
	db := openAcceptanceDatabase(t, database)
	defer func() { _ = db.Close() }()
	var state acceptanceOperationState
	if err := db.QueryRow(`SELECT status, work_attempt, work_lease_token FROM knowl_operations WHERE operation_id = ?`, id).
		Scan(&state.Status, &state.WorkAttempt, &state.WorkLeaseToken); err != nil {
		t.Fatalf("read durable operation state: %v", err)
	}
	return state
}

func assertOperationCount(t *testing.T, database string, want int) {
	t.Helper()
	db := openAcceptanceDatabase(t, database)
	defer func() { _ = db.Close() }()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowl_operations`).Scan(&got); err != nil {
		t.Fatalf("count durable operations: %v", err)
	}
	if got != want {
		t.Fatalf("durable operation count = %d, want %d", got, want)
	}
}

func expireAcceptanceLease(t *testing.T, database string, id domain.OperationID) {
	t.Helper()
	db := openAcceptanceDatabase(t, database)
	defer func() { _ = db.Close() }()
	result, err := db.Exec(`UPDATE knowl_operations SET work_lease_expires_at = ? WHERE operation_id = ?`, time.Unix(1, 0).UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		t.Fatalf("expire durable work lease: %v", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		t.Fatalf("expired lease rows = %d, err = %v", rows, err)
	}
}

func openAcceptanceDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open acceptance database: %v", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		_ = db.Close()
		t.Fatalf("configure acceptance database: %v", err)
	}
	return db
}

func assertInvocationCount(t *testing.T, directory string, want int) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, "invocation-*"))
	if err != nil {
		t.Fatalf("list provider invocations: %v", err)
	}
	if len(matches) != want {
		t.Fatalf("provider invocation count = %d, want %d", len(matches), want)
	}
}

func assertCommittedWorkspace(t *testing.T, project acceptanceProject, want map[domain.PageID]string) {
	t.Helper()
	if err := project.workspace.Validate(); err != nil {
		t.Fatalf("validate committed workspace: %v", err)
	}
	ids := make([]domain.PageID, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	pages, err := project.workspace.ReadPages(context.Background(), knowlruntime.DefaultScope, ids, domain.ReadLimits{})
	if err != nil {
		t.Fatalf("read committed pages: %v", err)
	}
	if len(pages) != len(want) {
		t.Fatalf("committed pages = %#v, want %d", pages, len(want))
	}
	for _, page := range pages {
		if page.Title != want[page.ID] {
			t.Fatalf("page %q title = %q, want %q", page.ID, page.Title, want[page.ID])
		}
	}
}
