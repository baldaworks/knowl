package main

import (
	"bytes"
	"context"
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

const commandEngineeringSourceID = "engineering"

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
}

type stubLocalSourceHost struct {
	sources      []domain.Source
	syncID       domain.SourceID
	statusID     domain.SourceID
	syncAllCalls int
	stopCalls    int
	syncErr      error
	stopErr      error
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

func (host *stubLocalSourceHost) Stop(context.Context) error {
	host.stopCalls++
	return host.stopErr
}
