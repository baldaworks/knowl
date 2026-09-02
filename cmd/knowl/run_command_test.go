package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	knowlruntime "github.com/baldaworks/knowl/pkg/knowl"
)

type stubRunHost struct {
	runOnceCalls   int
	stopCalls      int
	gotOptions     knowlruntime.RunOnceOptions
	result         knowlruntime.RunOnceResult
	runErr         error
	stopErr        error
}

const testRunSourceID = "src-1"

func (host *stubRunHost) RunOnce(_ context.Context, options knowlruntime.RunOnceOptions) (knowlruntime.RunOnceResult, error) {
	host.runOnceCalls++
	host.gotOptions = options
	return host.result, host.runErr
}

func (host *stubRunHost) Stop(context.Context) error {
	host.stopCalls++
	return host.stopErr
}

func TestRunCommandExecution(t *testing.T) {
	original := newLocalRunSession
	t.Cleanup(func() { newLocalRunSession = original })

	stub := &stubRunHost{
		result: knowlruntime.RunOnceResult{
			Sources: []knowlruntime.SourceSyncResult{
				{SourceID: testRunSourceID, Changed: true},
			},
			Operations: knowlruntime.DrainResult{
				Completed: 2,
				Total:     2,
			},
		},
	}
	newLocalRunSession = func(context.Context) (localRunSession, error) {
		return localRunSession{
			Host:            stub,
			ShutdownTimeout: time.Second,
		}, nil
	}

	cmd := newRunCommand()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--source", testRunSourceID, "--no-hierarchy"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	if stub.runOnceCalls != 1 {
		t.Errorf("RunOnce calls = %d, want 1", stub.runOnceCalls)
	}
	if stub.stopCalls != 1 {
		t.Errorf("Stop calls = %d, want 1", stub.stopCalls)
	}
	if stub.gotOptions.SourceID != testRunSourceID {
		t.Errorf("got SourceID = %q, want %q", stub.gotOptions.SourceID, testRunSourceID)
	}
	if !stub.gotOptions.SyncSources {
		t.Errorf("got SyncSources = false, want true")
	}
	if !stub.gotOptions.DrainOperations {
		t.Errorf("got DrainOperations = false, want true")
	}
	if stub.gotOptions.ReconcileHierarchy {
		t.Errorf("got ReconcileHierarchy = true, want false")
	}

	var output knowlruntime.RunOnceResult
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode stdout JSON: %v", err)
	}
	if len(output.Sources) != 1 || output.Sources[0].SourceID != testRunSourceID {
		t.Errorf("decoded Sources = %#v", output.Sources)
	}
	if output.Operations.Completed != 2 {
		t.Errorf("decoded Operations = %#v", output.Operations)
	}
}

func TestRunCommandErrorPropagation(t *testing.T) {
	original := newLocalRunSession
	t.Cleanup(func() { newLocalRunSession = original })

	expectedErr := errors.New("sync failed")
	stub := &stubRunHost{
		runErr: expectedErr,
	}
	newLocalRunSession = func(context.Context) (localRunSession, error) {
		return localRunSession{
			Host:            stub,
			ShutdownTimeout: time.Second,
		}, nil
	}

	cmd := newRunCommand()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if !errors.Is(err, expectedErr) {
		t.Fatalf("Execute() error = %v, want %v", err, expectedErr)
	}
	if stub.stopCalls != 1 {
		t.Errorf("Stop calls = %d, want 1", stub.stopCalls)
	}
}
