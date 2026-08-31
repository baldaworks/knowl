package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	commandEngineeringSourceID = "engineering"
	failureClassFlag           = "--failure-class"
	providerFailureClass       = "provider"
)

func TestSourceCommandsValidateBeforeSessionAndEmitRedactedJSON(t *testing.T) {
	original := newLocalSourceSession
	t.Cleanup(func() { newLocalSourceSession = original })
	host := &stubLocalSourceHost{sources: []domain.Source{{
		ID: commandEngineeringSourceID, Type: domain.SourceTypeFilesystem, Enabled: true,
		Config: domain.SourceConfig{Filesystem: &domain.FilesystemSourceConfig{Root: "/secret/source/root"}},
		Sync:   domain.SourceSyncPolicy{OnStart: true, Interval: time.Minute},
	}}}
	sessions := 0
	newLocalSourceSession = func(context.Context) (localSourceSession, error) {
		sessions++
		return localSourceSession{Host: host, ShutdownTimeout: time.Second}, nil
	}

	for _, args := range [][]string{{}, {sourceSyncAllFlag, commandEngineeringSourceID}, {commandEngineeringSourceID, commandOperationsSourceID}} {
		command := newSourceSyncCommand()
		command.SetArgs(args)
		if err := command.Execute(); err == nil {
			t.Fatalf("sync args %#v unexpectedly succeeded", args)
		}
	}
	if sessions != 0 {
		t.Fatalf("invalid sync constructed %d sessions", sessions)
	}
	host.syncErr = app.ErrSourceInvalid
	var rejected bytes.Buffer
	rejectedSync := newSourceSyncCommand()
	rejectedSync.SetOut(&rejected)
	rejectedSync.SetArgs([]string{"secret-invalid-id"})
	if err := rejectedSync.Execute(); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("rejected sync error = %v", err)
	}
	if rejected.Len() != 0 {
		t.Fatalf("rejected sync echoed input: %s", rejected.String())
	}
	if host.stopCalls != 1 {
		t.Fatalf("rejected sync stop calls = %d", host.stopCalls)
	}
	host.syncErr = nil

	var output bytes.Buffer
	list := newSourceListCommand()
	list.SetOut(&output)
	if err := list.Execute(); err != nil {
		t.Fatalf("source list: %v", err)
	}
	if !strings.Contains(output.String(), `"id":"`+commandEngineeringSourceID+`"`) || strings.Contains(output.String(), "/secret/source/root") {
		t.Fatalf("source list JSON is missing identity or leaks root: %s", output.String())
	}
	if host.stopCalls != 2 {
		t.Fatalf("source list stop calls = %d", host.stopCalls)
	}

	output.Reset()
	syncOne := newSourceSyncCommand()
	syncOne.SetOut(&output)
	syncOne.SetArgs([]string{commandEngineeringSourceID})
	if err := syncOne.Execute(); err != nil {
		t.Fatalf("source sync: %v", err)
	}
	if host.syncID != commandEngineeringSourceID || !strings.Contains(output.String(), `"source_id":"`+commandEngineeringSourceID+`"`) {
		t.Fatalf("source sync = id %q, JSON %s", host.syncID, output.String())
	}

	output.Reset()
	syncAll := newSourceSyncCommand()
	syncAll.SetOut(&output)
	syncAll.SetArgs([]string{sourceSyncAllFlag})
	if err := syncAll.Execute(); err != nil {
		t.Fatalf("source sync --all: %v", err)
	}
	if host.syncAllCalls != 1 || !strings.Contains(output.String(), `"results"`) {
		t.Fatalf("source sync all = calls %d, JSON %s", host.syncAllCalls, output.String())
	}

	output.Reset()
	status := newSourceStatusCommand()
	status.SetOut(&output)
	status.SetArgs([]string{commandEngineeringSourceID})
	if err := status.Execute(); err != nil {
		t.Fatalf("source status: %v", err)
	}
	if host.statusID != commandEngineeringSourceID || !strings.Contains(output.String(), `"status":"succeeded"`) || !strings.Contains(output.String(), `"maintenance"`) || !strings.Contains(output.String(), `"operation_id":"maintenance-operation"`) {
		t.Fatalf("source status = id %q, JSON %s", host.statusID, output.String())
	}
}

func TestSourceCommandPreservesOperationAndStopErrors(t *testing.T) {
	original := newLocalSourceSession
	t.Cleanup(func() { newLocalSourceSession = original })
	operationErr := errors.New("source operation failed")
	stopErr := errors.New("source stop failed")
	host := &stubLocalSourceHost{syncErr: operationErr, stopErr: stopErr}
	newLocalSourceSession = func(context.Context) (localSourceSession, error) {
		return localSourceSession{Host: host, ShutdownTimeout: time.Second}, nil
	}
	command := newSourceSyncCommand()
	command.SetArgs([]string{commandEngineeringSourceID})
	err := command.Execute()
	if !errors.Is(err, operationErr) || !errors.Is(err, stopErr) {
		t.Fatalf("source command error = %v", err)
	}
}

func TestSourceRetryCommandValidatesBeforeSessionAndEmitsStructuredResult(t *testing.T) {
	original := newLocalSourceSession
	t.Cleanup(func() { newLocalSourceSession = original })
	host := &stubLocalSourceHost{retryResult: app.SourceMaintenanceRetryResult{
		SourceID: commandEngineeringSourceID, DryRun: true, Matched: 2,
		OperationIDs: []domain.OperationID{"maintenance-a", "maintenance-b"},
	}}
	sessions := 0
	newLocalSourceSession = func(context.Context) (localSourceSession, error) {
		sessions++
		return localSourceSession{Host: host, ShutdownTimeout: time.Second}, nil
	}

	for _, args := range [][]string{
		{commandEngineeringSourceID},
		{"INVALID SECRET", failureClassFlag, providerFailureClass},
		{commandEngineeringSourceID, failureClassFlag, "provider secret"},
	} {
		command := newSourceRetryCommand()
		command.SetArgs(args)
		if err := command.Execute(); err == nil {
			t.Fatalf("retry args %#v unexpectedly succeeded", args)
		}
	}
	if sessions != 0 {
		t.Fatalf("invalid retry constructed %d sessions", sessions)
	}

	var output bytes.Buffer
	command := newSourceRetryCommand()
	command.SetOut(&output)
	command.SetArgs([]string{commandEngineeringSourceID, failureClassFlag, providerFailureClass, failureClassFlag, providerFailureClass, "--dry-run"})
	if err := command.Execute(); err != nil {
		t.Fatalf("source retry: %v", err)
	}
	if host.retryID != commandEngineeringSourceID || !host.retryDryRun || len(host.retryClasses) != 1 || host.retryClasses[0] != providerFailureClass {
		t.Fatalf("source retry request = id %q classes %#v dry-run %v", host.retryID, host.retryClasses, host.retryDryRun)
	}
	for _, required := range []string{`"matched":2`, `"requeued":0`, `"operation_ids":["maintenance-a","maintenance-b"]`} {
		if !strings.Contains(output.String(), required) {
			t.Fatalf("source retry JSON missing %s: %s", required, output.String())
		}
	}
	if host.stopCalls != 1 {
		t.Fatalf("source retry stop calls = %d", host.stopCalls)
	}

	host.retryErr = app.ErrSourceRetryConflict
	host.retryResult = app.SourceMaintenanceRetryResult{
		SourceID: commandEngineeringSourceID, Matched: 2, Rejected: 1,
		OperationIDs: []domain.OperationID{"maintenance-a", "maintenance-invalid"},
	}
	output.Reset()
	rejected := newSourceRetryCommand()
	rejected.SetOut(&output)
	rejected.SetArgs([]string{commandEngineeringSourceID, failureClassFlag, providerFailureClass})
	if err := rejected.Execute(); !errors.Is(err, app.ErrSourceRetryConflict) || !strings.Contains(output.String(), `"rejected":1`) {
		t.Fatalf("rejected retry = %v, JSON %s", err, output.String())
	}
}

func TestSourceCommandsUseRealHostWithMaintainer(t *testing.T) {
	original := newLocalSourceSession
	t.Cleanup(func() { newLocalSourceSession = original })
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	sourceRoot := t.TempDir()
	target := filepath.Join(sourceRoot, "docs", "Runbook.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("# Providerfreebeacon\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.Sources = []domain.Source{{
		ID: commandEngineeringSourceID, Type: domain.SourceTypeFilesystem, Enabled: true,
		Config: domain.SourceConfig{Filesystem: &domain.FilesystemSourceConfig{Root: sourceRoot, Flavor: domain.SourceFlavorMarkdown}},
		Sync:   domain.SourceSyncPolicy{OnStart: true},
	}}
	newLocalSourceSession = func(ctx context.Context) (localSourceSession, error) {
		host, hostErr := knowl.New(ctx, knowl.Options{Config: config, Maintainer: provider.Fixture{}})
		return localSourceSession{Host: host, ShutdownTimeout: time.Second}, hostErr
	}

	var output bytes.Buffer
	syncCommand := newSourceSyncCommand()
	syncCommand.SetOut(&output)
	syncCommand.SetArgs([]string{commandEngineeringSourceID})
	if err := syncCommand.Execute(); err != nil {
		t.Fatalf("source sync: %v", err)
	}
	if !strings.Contains(output.String(), `"changed":true`) {
		t.Fatalf("sync JSON: %s", output.String())
	}

	output.Reset()
	statusCommand := newSourceStatusCommand()
	statusCommand.SetOut(&output)
	statusCommand.SetArgs([]string{commandEngineeringSourceID})
	if err := statusCommand.Execute(); err != nil {
		t.Fatalf("source status: %v", err)
	}
	if !strings.Contains(output.String(), `"status":"succeeded"`) {
		t.Fatalf("status JSON: %s", output.String())
	}
	var status domain.SourceStatus
	if err := json.Unmarshal(output.Bytes(), &status); err != nil || len(status.Maintenance.Samples) != 1 {
		t.Fatalf("decode source status = %#v, err = %v, JSON %s", status, err, output.String())
	}
	operationID := status.Maintenance.Samples[0].OperationID
	recoveryHost, err := knowl.New(context.Background(), knowl.Options{Config: config, Maintainer: provider.Fixture{}})
	if err != nil {
		t.Fatalf("open recovery host: %v", err)
	}
	if err := recoveryHost.Operations().Fail(context.Background(), operationID, domain.Failure{
		Class: providerFailureClass, Reason: "provider_run", OperationID: string(operationID),
	}); err != nil {
		t.Fatalf("seed terminal provider failure: %v", err)
	}
	if err := recoveryHost.Stop(context.Background()); err != nil {
		t.Fatalf("close recovery host: %v", err)
	}

	output.Reset()
	previewCommand := newSourceRetryCommand()
	previewCommand.SetOut(&output)
	previewCommand.SetArgs([]string{commandEngineeringSourceID, failureClassFlag, providerFailureClass, "--dry-run"})
	if err := previewCommand.Execute(); err != nil {
		t.Fatalf("source retry preview: %v", err)
	}
	var preview app.SourceMaintenanceRetryResult
	if err := json.Unmarshal(output.Bytes(), &preview); err != nil || preview.Matched != 1 || preview.Requeued != 0 ||
		len(preview.OperationIDs) != 1 || preview.OperationIDs[0] != operationID {
		t.Fatalf("source retry preview = %#v, err = %v, JSON %s", preview, err, output.String())
	}

	output.Reset()
	retryCommand := newSourceRetryCommand()
	retryCommand.SetOut(&output)
	retryCommand.SetArgs([]string{commandEngineeringSourceID, failureClassFlag, providerFailureClass})
	if err := retryCommand.Execute(); err != nil {
		t.Fatalf("source retry: %v", err)
	}
	var retried app.SourceMaintenanceRetryResult
	if err := json.Unmarshal(output.Bytes(), &retried); err != nil || retried.Matched != 1 || retried.Requeued != 1 {
		t.Fatalf("source retry result = %#v, err = %v, JSON %s", retried, err, output.String())
	}

	output.Reset()
	statusCommand = newSourceStatusCommand()
	statusCommand.SetOut(&output)
	statusCommand.SetArgs([]string{commandEngineeringSourceID})
	if err := statusCommand.Execute(); err != nil {
		t.Fatalf("source status after retry: %v", err)
	}
	status = domain.SourceStatus{}
	if err := json.Unmarshal(output.Bytes(), &status); err != nil || status.Maintenance.Counts.Queued != 1 ||
		status.Maintenance.Counts.Failed != 0 || len(status.Maintenance.Samples) != 1 ||
		status.Maintenance.Samples[0].OperationID != operationID || status.Maintenance.Samples[0].ManualRetryCount != 1 {
		t.Fatalf("source status after retry = %#v, err = %v, JSON %s", status.Maintenance, err, output.String())
	}
}

type stubLocalSourceHost struct {
	sources      []domain.Source
	syncID       domain.SourceID
	statusID     domain.SourceID
	syncAllCalls int
	stopCalls    int
	syncErr      error
	stopErr      error
	retryID      domain.SourceID
	retryClasses []string
	retryDryRun  bool
	retryResult  app.SourceMaintenanceRetryResult
	retryErr     error
}

func (host *stubLocalSourceHost) Sources() []domain.Source { return host.sources }

func (host *stubLocalSourceHost) SyncSource(_ context.Context, id domain.SourceID) (knowl.SourceSyncResult, error) {
	host.syncID = id
	return knowl.SourceSyncResult{SourceID: id}, host.syncErr
}

func (host *stubLocalSourceHost) SyncAll(context.Context) (knowl.SourceSyncAllResult, error) {
	host.syncAllCalls++
	return knowl.SourceSyncAllResult{Results: []knowl.SourceSyncResult{}}, host.syncErr
}

func (host *stubLocalSourceHost) SourceStatus(_ context.Context, id domain.SourceID) (domain.SourceStatus, error) {
	host.statusID = id
	return domain.SourceStatus{
		SourceID: id, Status: domain.SyncStatusSucceeded,
		Maintenance: domain.SourceMaintenanceStatus{
			Counts:  domain.MaintenanceCounts{Queued: 1},
			Samples: []domain.MaintenanceSample{{DocumentID: "docs/page.md", Revision: "revision-1", OperationID: "maintenance-operation", Status: domain.StatusReceived}},
		},
	}, nil
}

func (host *stubLocalSourceHost) RetrySourceMaintenance(_ context.Context, id domain.SourceID, failureClasses []string, dryRun bool) (app.SourceMaintenanceRetryResult, error) {
	host.retryID = id
	host.retryClasses = append([]string(nil), failureClasses...)
	host.retryDryRun = dryRun
	result := host.retryResult
	if result.SourceID == "" {
		result.SourceID = id
	}
	if result.OperationIDs == nil {
		result.OperationIDs = make([]domain.OperationID, 0)
	}
	return result, host.retryErr
}

func (host *stubLocalSourceHost) Stop(context.Context) error {
	host.stopCalls++
	return host.stopErr
}
