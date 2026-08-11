package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/baldaworks/knowl/internal/httpapi/trustedrequest"
	"github.com/spf13/cobra"
)

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
