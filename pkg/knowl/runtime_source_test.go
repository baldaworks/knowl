package knowl_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	sourcefilesystem "github.com/baldaworks/knowl/internal/source/filesystem"
	knowl "github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	runtimeAlphaSourceID = domain.SourceID("alpha")
	runtimeBetaSourceID  = domain.SourceID("beta")
	runtimeZetaSourceID  = domain.SourceID("zeta")
)

func TestPrepareReadOnlyDoesNotRunOnStartSourceSync(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	adapter := &runtimeSourceAdapter{}
	source := runtimeFilesystemSource(runtimeAlphaSourceID, t.TempDir(), true)
	source.Sync = domain.SourceSyncPolicy{OnStart: true}
	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.Sources = []domain.Source{source}
	host, err := knowl.New(ctx, knowl.Options{
		Config: config, Maintainer: provider.Fixture{},
		SourceAdapters: map[domain.SourceType]app.SourceAdapter{
			domain.SourceTypeFilesystem: adapter,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := host.PrepareReadOnly(); err != nil {
		t.Fatalf("PrepareReadOnly() error: %v", err)
	}
	body, status, err := doHostRequest(t, host, http.MethodGet, "/v1/retrieve?query=absentbeacon", nil)
	if err != nil || status != http.StatusOK || !strings.Contains(string(body), `"evidence":[]`) {
		t.Fatalf("read-only retrieve = %d, %v, %s", status, err, body)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := host.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
	if lists, fetches := adapter.calls(); lists != 0 || fetches != 0 {
		t.Fatalf("read-only host source calls = (%d lists, %d fetches), want zero", lists, fetches)
	}
}

func TestRetrySourceMaintenanceUsesConfiguredSourceWithoutStartingBackgroundJobs(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	adapter := &runtimeSourceAdapter{}
	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.Sources = []domain.Source{runtimeFilesystemSource(runtimeAlphaSourceID, t.TempDir(), true)}
	host, err := knowl.New(ctx, knowl.Options{
		Config: config, Maintainer: provider.Fixture{},
		SourceAdapters: map[domain.SourceType]app.SourceAdapter{domain.SourceTypeFilesystem: adapter},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = host.Stop(stopCtx)
	})
	if _, err := host.SyncSource(ctx, runtimeAlphaSourceID); err != nil {
		t.Fatalf("sync source fixture: %v", err)
	}
	status, err := host.SourceStatus(ctx, runtimeAlphaSourceID)
	if err != nil || len(status.Maintenance.Samples) != 1 {
		t.Fatalf("source maintenance fixture = %#v, err = %v", status.Maintenance, err)
	}
	id := status.Maintenance.Samples[0].OperationID
	if err := host.Operations().Fail(ctx, id, domain.Failure{Class: hostProviderID, Reason: "provider_run", OperationID: string(id)}); err != nil {
		t.Fatalf("fail maintenance fixture: %v", err)
	}

	preview, err := host.RetrySourceMaintenance(ctx, runtimeAlphaSourceID, []string{hostProviderID}, true)
	if err != nil || preview.Matched != 1 || preview.Requeued != 0 || len(preview.OperationIDs) != 1 || preview.OperationIDs[0] != id {
		t.Fatalf("host retry preview = %#v, err = %v", preview, err)
	}
	requeued, err := host.RetrySourceMaintenance(ctx, runtimeAlphaSourceID, []string{hostProviderID}, false)
	if err != nil || requeued.Requeued != 1 {
		t.Fatalf("host retry mutation = %#v, err = %v", requeued, err)
	}
	if err := host.PrepareReadOnly(); err != nil {
		t.Fatalf("prepare read-only host: %v", err)
	}
	operation, err := host.Operations().Operation(ctx, config.Scope, id)
	if err != nil || operation.Status != domain.StatusReceived || operation.ManualRetryCount != 1 {
		t.Fatalf("requeued host operation = %#v, err = %v", operation, err)
	}
	status, err = host.SourceStatus(ctx, runtimeAlphaSourceID)
	if err != nil || status.Maintenance.Counts.Queued != 1 || status.Maintenance.Counts.Failed != 0 ||
		len(status.Maintenance.Samples) != 1 || status.Maintenance.Samples[0].ManualRetryCount != 1 {
		t.Fatalf("source status after retry = %#v, err = %v", status.Maintenance, err)
	}
	if lists, fetches := adapter.calls(); lists != 1 || fetches != 1 {
		t.Fatalf("retry command started background source calls = (%d, %d)", lists, fetches)
	}
	if _, err := host.RetrySourceMaintenance(ctx, "missing", []string{hostProviderID}, false); !errors.Is(err, app.ErrSourceNotFound) {
		t.Fatalf("unknown source retry error = %v", err)
	}
}

func TestSourceRuntimeVerticalAndFailureIsolation(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	engineeringRoot := t.TempDir()
	operationsRoot := t.TempDir()
	writeRuntimeSourceFile(t, engineeringRoot, "docs/Shared.md", "# Verticalbeacon\n")
	writeRuntimeSourceFile(t, operationsRoot, "docs/Shared.md", "# Mustnotappear\n")
	engineering := runtimeFilesystemSource("engineering", engineeringRoot, true)
	engineering.Sync = domain.SourceSyncPolicy{OnStart: true, RetryInitial: time.Hour, RetryMaximum: time.Hour}
	operations := runtimeFilesystemSource("operations", operationsRoot, true)
	operations.Sync = domain.SourceSyncPolicy{OnStart: true, RetryInitial: time.Hour, RetryMaximum: time.Hour}
	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.ListenAddr = hostListenAddr
	config.Sources = []domain.Source{operations, engineering}
	const secretFailure = "secret-adapter-credential"
	observer := &runtimeAttemptObserver{events: make(chan knowl.SourceAttempt, 8)}
	adapter := &isolatingRuntimeSourceAdapter{
		good: sourcefilesystem.NewDefault(), failingSource: "operations", failure: errors.New(secretFailure),
	}
	host, err := knowl.New(ctx, knowl.Options{
		Config: config, Maintainer: provider.Fixture{}, SourceObserver: observer,
		SourceAdapters: map[domain.SourceType]app.SourceAdapter{domain.SourceTypeFilesystem: adapter},
	})
	if err != nil {
		t.Fatalf("compose vertical Host: %v", err)
	}
	if err := host.Start(ctx); err != nil {
		t.Fatalf("start vertical Host: %v", err)
	}
	defer shutdownHost(t, host)
	seen := map[domain.SourceID]knowl.SourceAttempt{}
	deadline := time.After(5 * time.Second)
	for len(seen) < 2 {
		select {
		case attempt := <-observer.events:
			if attempt.Trigger == knowl.SourceTriggerOnStart {
				seen[attempt.SourceID] = attempt
			}
		case <-deadline:
			t.Fatalf("on-start attempts = %#v", seen)
		}
	}
	if seen["engineering"].Result != "succeeded" || seen["operations"].Result != "failed" || seen["operations"].FailureClass == "" {
		t.Fatalf("isolated attempts = %#v", seen)
	}
	if !host.Ready() {
		t.Fatal("source failure changed Host readiness")
	}
	if _, status, _ := doHostRequest(t, host, http.MethodGet, "/healthz", nil); status != http.StatusOK {
		t.Fatalf("health status = %d", status)
	}
	if _, status, _ := doHostRequest(t, host, http.MethodGet, "/readyz", nil); status != http.StatusOK {
		t.Fatalf("ready status = %d", status)
	}
	httpBody, status, err := doHostRequest(t, host, http.MethodGet, "/v1/retrieve?query=Verticalbeacon&source=engineering", nil)
	if err != nil || status != http.StatusOK || !strings.Contains(string(httpBody), `"evidence":[]`) {
		t.Fatalf("HTTP retrieve = %d, %v, %s", status, err, httpBody)
	}
	mcpValue, err := host.MCP().Call(ctx, hostRetrieveToolName, map[string]any{
		hostQueryKey: "Verticalbeacon", "sources": []any{"engineering"},
	})
	mcpJSON, marshalErr := json.Marshal(mcpValue)
	if err != nil || marshalErr != nil || !strings.Contains(string(mcpJSON), `"evidence":[]`) {
		t.Fatalf("MCP retrieve = %#v, %v", mcpValue, err)
	}
	if raw, ok := runtimeRawDocumentContent(t, host, config.Scope, "engineering", "docs/Shared.md"); !ok || !strings.Contains(string(raw), "Verticalbeacon") {
		t.Fatalf("engineering raw source = %q, present=%v", raw, ok)
	}
	if inventory := runtimeSourceInventory(t, host, config.Scope, "engineering"); len(inventory) != 0 {
		t.Fatalf("configured source leaked into LLM wiki: %#v", inventory)
	}
	if report, err := host.Lint().Lint(ctx, config.Scope); err != nil || report.Scope != config.Scope {
		t.Fatalf("lint = %#v, %v", report, err)
	}
	goodStatus, err := host.SourceStatus(ctx, "engineering")
	if err != nil || goodStatus.Status != domain.SyncStatusSucceeded {
		t.Fatalf("engineering status = %#v, %v", goodStatus, err)
	}
	badStatus, err := host.SourceStatus(ctx, "operations")
	if err != nil || badStatus.Status != domain.SyncStatusFailed {
		t.Fatalf("operations status = %#v, %v", badStatus, err)
	}
	redacted, err := json.Marshal(struct {
		Attempt knowl.SourceAttempt `json:"attempt"`
		Status  domain.SourceStatus `json:"status"`
	}{Attempt: seen["operations"], Status: badStatus})
	if err != nil || strings.Contains(string(redacted), secretFailure) {
		t.Fatalf("source failure leaked secret: %s, %v", redacted, err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := host.Stop(stopCtx); err != nil {
		t.Fatalf("vertical shutdown: %v", err)
	}
	if host.Ready() {
		t.Fatal("Host remained ready after shutdown")
	}
}

func TestHostSourceRegistryAndProductionSync(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	activeRoot := t.TempDir()
	disabledRoot := t.TempDir()
	writeRuntimeSourceFile(t, activeRoot, "docs/Page.md", "# Productionbeacon\n")
	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.Sources = []domain.Source{
		runtimeFilesystemSource(runtimeZetaSourceID, activeRoot, true),
		runtimeFilesystemSource(runtimeAlphaSourceID, disabledRoot, false),
	}

	host, err := knowl.New(ctx, knowl.Options{Config: config, Maintainer: provider.Fixture{}})
	if err != nil {
		t.Fatalf("compose source Host: %v", err)
	}
	listed := host.Sources()
	if len(listed) != 2 || listed[0].ID != runtimeAlphaSourceID || listed[1].ID != runtimeZetaSourceID {
		t.Fatalf("sources = %#v, want alpha/zeta", listed)
	}
	listed[0].Config.Filesystem.Include[0] = "mutated"
	if host.Sources()[0].Config.Filesystem.Include[0] == "mutated" {
		t.Fatal("Sources returned mutable registry state")
	}

	if _, err := host.SyncSource(ctx, "INVALID SECRET"); !errors.Is(err, app.ErrSourceInvalid) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("invalid source error = %v", err)
	}
	if _, err := host.SyncSource(ctx, "unknown"); !errors.Is(err, app.ErrSourceNotFound) {
		t.Fatalf("unknown source error = %v", err)
	}
	if _, err := host.SyncSource(ctx, runtimeAlphaSourceID); !errors.Is(err, app.ErrSourceInvalid) {
		t.Fatalf("disabled source error = %v", err)
	}
	result, err := host.SyncSource(ctx, runtimeZetaSourceID)
	if err != nil || !result.Changed || result.SourceID != runtimeZetaSourceID || result.Run.Status != domain.SyncStatusSucceeded {
		t.Fatalf("SyncSource = %#v, %v", result, err)
	}
	status, err := host.SourceStatus(ctx, runtimeZetaSourceID)
	if err != nil || status.SourceID != runtimeZetaSourceID || status.Status != domain.SyncStatusSucceeded {
		t.Fatalf("SourceStatus = %#v, %v", status, err)
	}
	query, err := host.Query().Query(ctx, config.Scope, "Productionbeacon", domain.ReadLimits{}, []domain.SourceID{runtimeZetaSourceID})
	if err != nil || len(query.Pages) != 0 {
		t.Fatalf("source query = %#v, %v", query, err)
	}
	if raw, ok := runtimeRawDocumentContent(t, host, config.Scope, runtimeZetaSourceID, "docs/Page.md"); !ok || !strings.Contains(string(raw), "Productionbeacon") {
		t.Fatalf("production raw source = %q, present=%v", raw, ok)
	}
	if inventory := runtimeSourceInventory(t, host, config.Scope, runtimeZetaSourceID); len(inventory) != 0 {
		t.Fatalf("production source mirrors = %#v", inventory)
	}
	all, err := host.SyncAll(ctx)
	if err != nil || len(all.Results) != 1 || all.Results[0].SourceID != runtimeZetaSourceID {
		t.Fatalf("SyncAll = %#v, %v", all, err)
	}
	if err := host.Stop(ctx); err != nil {
		t.Fatalf("stop source Host: %v", err)
	}
	if _, err := host.SyncSource(ctx, runtimeZetaSourceID); !errors.Is(err, knowl.ErrHostClosed) {
		t.Fatalf("stopped SyncSource error = %v", err)
	}
}

func TestProductionHostMultiSourceLifecycleAcceptance(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	const curatedIndex = "---\nokf_version: \"0.2\"\n---\n# Curated Index\n"
	if err := os.WriteFile(filepath.Join(workspace.Root(), "wiki", "index.md"), []byte(curatedIndex), 0o600); err != nil {
		t.Fatal(err)
	}

	alphaRoot := t.TempDir()
	betaRoot := t.TempDir()
	writeRuntimeSourceFile(t, alphaRoot, "assets/logo.bin", "alpha-asset")
	writeRuntimeSourceFile(t, alphaRoot, "keep/alpha.md", "# Alpha keeper\n\nKeepalphabeacon\n")
	writeRuntimeSourceFile(t, alphaRoot, "shared/page.md", "# Alpha shared\n\nSharedacceptance alphabeacon\n")
	writeRuntimeSourceFile(t, betaRoot, "keep/beta.md", "# Beta keeper\n\nBetakeeper\n")
	writeRuntimeSourceFile(t, betaRoot, "shared/page.md", "# Beta shared\n\nSharedacceptance betabeacon\n")

	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "acceptance.db")
	config.ListenAddr = hostListenAddr
	config.Sources = []domain.Source{
		runtimeFilesystemSource(runtimeBetaSourceID, betaRoot, true),
		runtimeFilesystemSource(runtimeAlphaSourceID, alphaRoot, true),
	}
	for index := range config.Sources {
		config.Sources[index].Config.Filesystem.Include = []string{"**/*"}
	}
	limits := sourcefilesystem.DefaultLimits()
	limits.PageSize = 1
	productionAdapter, err := sourcefilesystem.New(limits)
	if err != nil {
		t.Fatal(err)
	}
	adapter := newAcceptanceRuntimeAdapter(productionAdapter)
	newHost := func() *knowl.Host {
		t.Helper()
		host, newErr := knowl.New(ctx, knowl.Options{
			Config: config, Maintainer: provider.Fixture{},
			SourceAdapters: map[domain.SourceType]app.SourceAdapter{
				domain.SourceTypeFilesystem: adapter,
			},
		})
		if newErr != nil {
			t.Fatalf("compose acceptance Host: %v", newErr)
		}
		return host
	}

	host := newHost()
	initial, err := host.SyncAll(ctx)
	if err != nil || len(initial.Results) != 2 || initial.Results[0].SourceID != runtimeAlphaSourceID || initial.Results[1].SourceID != runtimeBetaSourceID {
		t.Fatalf("initial SyncAll = %#v, %v", initial, err)
	}
	if adapter.fetchCount(runtimeAlphaSourceID) != 3 || adapter.fetchCount(runtimeBetaSourceID) != 2 {
		t.Fatalf("initial fetch counts = alpha:%d beta:%d", adapter.fetchCount(runtimeAlphaSourceID), adapter.fetchCount(runtimeBetaSourceID))
	}
	alphaShared := queryRuntimeSource(t, host, config.Scope, "Sharedacceptance", []domain.SourceID{runtimeAlphaSourceID})
	betaShared := queryRuntimeSource(t, host, config.Scope, "Sharedacceptance", []domain.SourceID{runtimeBetaSourceID})
	unfiltered := queryRuntimeSource(t, host, config.Scope, "Sharedacceptance", nil)
	if len(alphaShared) != 0 || len(betaShared) != 0 || len(unfiltered) != 0 {
		t.Fatalf("equal-path retrieval = alpha:%#v beta:%#v all:%#v", alphaShared, betaShared, unfiltered)
	}
	alphaRaw, alphaOK := runtimeRawDocumentContent(t, host, config.Scope, runtimeAlphaSourceID, "shared/page.md")
	betaRaw, betaOK := runtimeRawDocumentContent(t, host, config.Scope, runtimeBetaSourceID, "shared/page.md")
	if !alphaOK || !betaOK || !strings.Contains(string(alphaRaw), "alphabeacon") || !strings.Contains(string(betaRaw), "betabeacon") {
		t.Fatalf("equal-path raw lineage = alpha:%q/%v beta:%q/%v", alphaRaw, alphaOK, betaRaw, betaOK)
	}
	alphaInventory := runtimeSourceInventory(t, host, config.Scope, runtimeAlphaSourceID)
	betaInventory := runtimeSourceInventory(t, host, config.Scope, runtimeBetaSourceID)
	if len(alphaInventory) != 0 || len(betaInventory) != 0 {
		t.Fatalf("initial source mirrors = %#v / %#v", alphaInventory, betaInventory)
	}
	indexContent, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki", "index.md"))
	if err != nil || string(indexContent) != curatedIndex {
		t.Fatalf("initial sync changed curated index = %q, %v", indexContent, err)
	}

	adapter.resetFetches()
	repeated, err := host.SyncAll(ctx)
	if err != nil || repeated.Results[0].Changed || repeated.Results[1].Changed || adapter.fetchCount(runtimeAlphaSourceID) != 0 || adapter.fetchCount(runtimeBetaSourceID) != 0 {
		t.Fatalf("unchanged SyncAll = %#v, %v, fetches=%#v", repeated, err, adapter.fetchSnapshot())
	}
	if !reflect.DeepEqual(alphaInventory, runtimeSourceInventory(t, host, config.Scope, runtimeAlphaSourceID)) ||
		!reflect.DeepEqual(betaInventory, runtimeSourceInventory(t, host, config.Scope, runtimeBetaSourceID)) {
		t.Fatal("unchanged synchronization altered canonical source digests")
	}

	writeRuntimeSourceFile(t, alphaRoot, "shared/page.md", "# Alpha shared updated\n\nSharedacceptance alphaupdatedbeacon\n")
	adapter.resetFetches()
	updated, err := host.SyncSource(ctx, runtimeAlphaSourceID)
	if err != nil || !updated.Changed || updated.Run.Counts.Updated != 1 || adapter.fetchCount(runtimeAlphaSourceID) != 1 {
		t.Fatalf("selective update = %#v, %v, fetches=%d", updated, err, adapter.fetchCount(runtimeAlphaSourceID))
	}
	if raw, ok := runtimeRawDocumentContent(t, host, config.Scope, runtimeAlphaSourceID, "shared/page.md"); !ok || !strings.Contains(string(raw), "alphaupdatedbeacon") {
		t.Fatalf("updated alpha raw = %q, present=%v", raw, ok)
	}

	if err := os.Remove(filepath.Join(betaRoot, "shared", "page.md")); err != nil {
		t.Fatal(err)
	}
	deleted, err := host.SyncSource(ctx, runtimeBetaSourceID)
	if err != nil || !deleted.Changed || deleted.Run.Counts.Deleted != 1 {
		t.Fatalf("complete deletion = %#v, %v", deleted, err)
	}
	if len(queryRuntimeSource(t, host, config.Scope, "betabeacon", []domain.SourceID{runtimeBetaSourceID})) != 0 {
		t.Fatal("deleted beta page remained in active retrieval")
	}
	inspection, err := host.Workspace().Inspect(ctx, config.Scope)
	if err != nil {
		t.Fatal(err)
	}
	retainedRaw := false
	for _, raw := range inspection.RawSources {
		if raw.Source.Source.ID == "beta/shared/page.md" {
			content, readErr := host.Workspace().ReadSource(ctx, raw.Source, domain.ReadLimits{})
			if readErr == nil && strings.Contains(string(content), "betabeacon") {
				retainedRaw = true
			}
		}
	}
	if !retainedRaw {
		t.Fatal("deleted beta raw revision was not retained")
	}

	if err := os.Remove(filepath.Join(alphaRoot, "keep", "alpha.md")); err != nil {
		t.Fatal(err)
	}
	const secretFailure = "acceptance-secret-root-token"
	adapter.failAfterFirst(runtimeAlphaSourceID, errors.New(secretFailure))
	interrupted, err := host.SyncSource(ctx, runtimeAlphaSourceID)
	if err == nil || interrupted.FailureClass != "adapter" || strings.Contains(err.Error(), secretFailure) {
		t.Fatalf("interrupted alpha = %#v, %v", interrupted, err)
	}
	if raw, ok := runtimeRawDocumentContent(t, host, config.Scope, runtimeAlphaSourceID, "keep/alpha.md"); !ok || !strings.Contains(string(raw), "Keepalphabeacon") {
		t.Fatal("incomplete scan lost prior alpha raw evidence")
	}
	failedStatus, err := host.SourceStatus(ctx, runtimeAlphaSourceID)
	if err != nil || failedStatus.Status != domain.SyncStatusFailed {
		t.Fatalf("failed alpha status = %#v, %v", failedStatus, err)
	}
	if err := host.Stop(ctx); err != nil {
		t.Fatalf("stop first acceptance Host: %v", err)
	}

	adapter.clearFailure()
	restarted := newHost()
	defer shutdownHost(t, restarted)
	restartedStatus, err := restarted.SourceStatus(ctx, runtimeAlphaSourceID)
	if err != nil || restartedStatus.Status != domain.SyncStatusFailed || restartedStatus.LastSuccessfulRunID == "" {
		t.Fatalf("restarted alpha status = %#v, %v", restartedStatus, err)
	}
	if raw, ok := runtimeRawDocumentContent(t, restarted, config.Scope, runtimeAlphaSourceID, "keep/alpha.md"); !ok || !strings.Contains(string(raw), "Keepalphabeacon") {
		t.Fatal("restart lost prior raw evidence")
	}
	converged, err := restarted.SyncSource(ctx, runtimeAlphaSourceID)
	if err != nil || converged.Run.Counts.Deleted != 1 {
		t.Fatalf("post-restart convergence = %#v, %v", converged, err)
	}

	if err := restarted.Start(ctx); err != nil {
		t.Fatalf("start restarted acceptance Host: %v", err)
	}
	adapter.failAll(runtimeAlphaSourceID, errors.New(secretFailure))
	failed, err := restarted.SyncSource(ctx, runtimeAlphaSourceID)
	if err == nil || failed.FailureClass != "adapter" || strings.Contains(err.Error(), secretFailure) {
		t.Fatalf("isolated alpha failure = %#v, %v", failed, err)
	}
	healthy, err := restarted.SyncSource(ctx, runtimeBetaSourceID)
	if err != nil || healthy.Run.Status != domain.SyncStatusSucceeded || !restarted.Ready() {
		t.Fatalf("healthy beta after alpha failure = %#v, %v, ready=%v", healthy, err, restarted.Ready())
	}
	if raw, ok := runtimeRawDocumentContent(t, restarted, config.Scope, runtimeAlphaSourceID, "shared/page.md"); !ok || !strings.Contains(string(raw), "alphaupdatedbeacon") {
		t.Fatal("isolated source failure removed prior alpha raw evidence")
	}
	indexContent, err = os.ReadFile(filepath.Join(workspace.Root(), "wiki", "index.md"))
	if err != nil || string(indexContent) != curatedIndex {
		t.Fatalf("source lifecycle changed curated index = %q, %v", indexContent, err)
	}
}

func TestProductionHostBuildsSharedSemanticWikiFromConfiguredSources(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	alphaRoot := t.TempDir()
	betaRoot := t.TempDir()
	writeRuntimeSourceFile(t, alphaRoot, "architecture.md", "# Atlas\n\nAtlasintegrationbeacon uses PostgreSQL for durable state.\n")
	writeRuntimeSourceFile(t, betaRoot, "recovery.md", "# Atlas recovery\n\nAtlasintegrationbeacon recovery rebuilds projections.\n")

	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "semantic-sources.db")
	config.ListenAddr = hostListenAddr
	config.Sources = []domain.Source{
		runtimeFilesystemSource(runtimeBetaSourceID, betaRoot, true),
		runtimeFilesystemSource(runtimeAlphaSourceID, alphaRoot, true),
	}
	host, err := knowl.New(ctx, knowl.Options{Config: config, Maintainer: runtimeSharedAtlasMaintainer{}})
	if err != nil {
		t.Fatalf("compose semantic source Host: %v", err)
	}
	if err := host.Start(ctx); err != nil {
		t.Fatalf("start semantic source Host: %v", err)
	}
	defer shutdownHost(t, host)

	result, err := host.SyncAll(ctx)
	if err != nil || len(result.Results) != 2 || result.Results[0].SourceID != runtimeAlphaSourceID || result.Results[1].SourceID != runtimeBetaSourceID {
		t.Fatalf("SyncAll() = %#v, %v", result, err)
	}
	waitForRuntimeMaintenance(t, host, runtimeAlphaSourceID)
	waitForRuntimeMaintenance(t, host, runtimeBetaSourceID)

	alpha := queryRuntimeSource(t, host, config.Scope, "Atlasintegrationbeacon", []domain.SourceID{runtimeAlphaSourceID})
	beta := queryRuntimeSource(t, host, config.Scope, "Atlasintegrationbeacon", []domain.SourceID{runtimeBetaSourceID})
	all := queryRuntimeSource(t, host, config.Scope, "Atlasintegrationbeacon", nil)
	if len(alpha) != 1 || len(beta) != 1 || len(all) != 1 || alpha[0].ID != runtimeSharedAtlasPageID || beta[0].ID != runtimeSharedAtlasPageID {
		t.Fatalf("semantic retrieval = alpha:%#v beta:%#v all:%#v", alpha, beta, all)
	}
	documents := all[0].SourceDocuments
	if len(documents) != 2 || documents[0].SourceID != runtimeAlphaSourceID || documents[1].SourceID != runtimeBetaSourceID {
		t.Fatalf("semantic source documents = %#v", documents)
	}
	if inventory := runtimeSourceInventory(t, host, config.Scope, runtimeAlphaSourceID); len(inventory) != 0 {
		t.Fatalf("alpha source copied into semantic wiki: %#v", inventory)
	}
	if inventory := runtimeSourceInventory(t, host, config.Scope, runtimeBetaSourceID); len(inventory) != 0 {
		t.Fatalf("beta source copied into semantic wiki: %#v", inventory)
	}
	if raw, ok := runtimeRawDocumentContent(t, host, config.Scope, runtimeAlphaSourceID, "architecture.md"); !ok || !strings.Contains(string(raw), "durable state") {
		t.Fatalf("alpha raw evidence = %q, present=%v", raw, ok)
	}
	if raw, ok := runtimeRawDocumentContent(t, host, config.Scope, runtimeBetaSourceID, "recovery.md"); !ok || !strings.Contains(string(raw), "rebuilds projections") {
		t.Fatalf("beta raw evidence = %q, present=%v", raw, ok)
	}
}

const (
	runtimeSharedAtlasPageID   = domain.PageID("entities/atlas-integration")
	runtimeSharedAtlasPagePath = "wiki/entities/atlas-integration.md"
)

type runtimeSharedAtlasMaintainer struct{}

func (runtimeSharedAtlasMaintainer) Plan(_ context.Context, input domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	currentRef := app.SourceRefKey(input.Source)
	refs := []string{currentRef}
	expectedDigest := ""
	for _, page := range input.Pages {
		if page.ID != runtimeSharedAtlasPageID {
			continue
		}
		refs = append(refs, page.SourceRefs...)
		expectedDigest = page.Digest
	}
	sort.Strings(refs)
	refs = uniqueRuntimeRefs(refs)
	content := "---\nid: " + string(runtimeSharedAtlasPageID) + "\ntitle: Project Atlas\ntype: entity\nsource_refs:\n  - " +
		strings.Join(refs, "\n  - ") + "\n---\n# Project Atlas\n\nAtlasintegrationbeacon uses PostgreSQL for durable state; recovery rebuilds projections.\n"
	plan := domain.ModelEditPlan{
		SchemaDigest: input.Schema.Digest,
		SourceRefs:   refs,
		Edits: []domain.FileEdit{{
			Path: runtimeSharedAtlasPagePath, ExpectedDigest: expectedDigest, Content: []byte(content),
		}},
		Rationale: "merge related Atlas evidence into one semantic page",
	}
	if expectedDigest != "" {
		return plan, nil
	}
	for _, catalog := range input.Catalogs {
		if catalog.Path != "wiki/index.md" {
			continue
		}
		root := strings.TrimRight(catalog.Content, "\n") + "\n\n* [Project Atlas](entities/atlas-integration.md)\n"
		plan.Edits = append(plan.Edits, domain.FileEdit{
			Path: catalog.Path, ExpectedDigest: catalog.Digest, Content: []byte(root),
		})
		break
	}
	return plan, nil
}

func uniqueRuntimeRefs(refs []string) []string {
	unique := refs[:0]
	for _, ref := range refs {
		if len(unique) == 0 || unique[len(unique)-1] != ref {
			unique = append(unique, ref)
		}
	}
	return unique
}

func waitForRuntimeMaintenance(t *testing.T, host *knowl.Host, sourceID domain.SourceID) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := host.SourceStatus(context.Background(), sourceID)
		if err != nil {
			t.Fatalf("SourceStatus(%s): %v", sourceID, err)
		}
		if status.Maintenance.Counts.Failed > 0 {
			t.Fatalf("SourceStatus(%s) maintenance failed: %#v", sourceID, status.Maintenance)
		}
		if status.Maintenance.Counts.Committed == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("maintenance for %s did not commit", sourceID)
}

func TestHostProgrammaticFilesystemAdapterOverridesBuiltIn(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.Sources = []domain.Source{runtimeFilesystemSource("custom", t.TempDir(), true)}
	adapter := &runtimeSourceAdapter{}
	host, err := knowl.New(ctx, knowl.Options{
		Config: config, Maintainer: provider.Fixture{},
		SourceAdapters: map[domain.SourceType]app.SourceAdapter{
			domain.SourceTypeFilesystem: adapter,
		},
	})
	if err != nil {
		t.Fatalf("compose overridden source Host: %v", err)
	}
	defer shutdownHost(t, host)
	if _, err := host.SyncSource(ctx, "custom"); err != nil {
		t.Fatalf("sync overridden adapter: %v", err)
	}
	lists, fetches := adapter.calls()
	if lists == 0 || fetches != 1 {
		t.Fatalf("adapter calls = list:%d fetch:%d", lists, fetches)
	}
	query, err := host.Query().Query(ctx, config.Scope, "Overridebeacon", domain.ReadLimits{}, []domain.SourceID{"custom"})
	if err != nil || len(query.Pages) != 0 {
		t.Fatalf("override query = %#v, %v", query, err)
	}
	if raw, ok := runtimeRawDocumentContent(t, host, config.Scope, "custom", "custom.md"); !ok || !strings.Contains(string(raw), "Overridebeacon") {
		t.Fatalf("override raw source = %q, present=%v", raw, ok)
	}
}

func TestHostOnStartSourceDoesNotBlockReadinessAndStopsCleanly(t *testing.T) {
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	source := runtimeFilesystemSource("blocking", t.TempDir(), true)
	source.Sync.OnStart = true
	config.Sources = []domain.Source{source}
	adapter := &blockingRuntimeSourceAdapter{started: make(chan struct{})}
	host, err := knowl.New(context.Background(), knowl.Options{
		Config: config, Maintainer: provider.Fixture{},
		SourceAdapters: map[domain.SourceType]app.SourceAdapter{
			domain.SourceTypeFilesystem: adapter,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Start(context.Background()); err != nil {
		t.Fatalf("Start blocked on on-start source: %v", err)
	}
	if !host.Ready() {
		t.Fatal("Host not ready while on-start source is active")
	}
	select {
	case <-adapter.started:
	case <-time.After(time.Second):
		t.Fatal("on-start source did not begin")
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := host.Stop(stopCtx); err != nil {
		t.Fatalf("Stop did not join on-start source: %v", err)
	}
}

func TestHostRejectsInvalidSourceAdapterRegistrationBeforeOpeningStore(t *testing.T) {
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	for _, test := range []struct {
		name    string
		adapter app.SourceAdapter
	}{
		{name: "nil interface"},
		{name: "typed nil", adapter: (*runtimeSourceAdapter)(nil)},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, newErr := knowl.New(context.Background(), knowl.Options{
				Config: config, Maintainer: provider.Fixture{}, SourceAdapters: map[domain.SourceType]app.SourceAdapter{domain.SourceTypeFilesystem: test.adapter},
			})
			if !errors.Is(newErr, app.ErrSourceInvalid) {
				t.Fatalf("invalid adapter error = %v", newErr)
			}
			if _, statErr := os.Stat(config.StorePath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("store opened before adapter validation: %v", statErr)
			}
		})
	}
}

func runtimeFilesystemSource(id domain.SourceID, root string, enabled bool) domain.Source {
	return domain.Source{
		ID: id, Type: domain.SourceTypeFilesystem, Enabled: enabled,
		Config: domain.SourceConfig{Filesystem: &domain.FilesystemSourceConfig{Root: root, Flavor: domain.SourceFlavorMarkdown}},
	}
}

func writeRuntimeSourceFile(t *testing.T, root, relative, content string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

type runtimeSourceAdapter struct {
	mu      sync.Mutex
	lists   int
	fetches int
}

type runtimeAttemptObserver struct {
	events chan knowl.SourceAttempt
}

func (observer *runtimeAttemptObserver) ObserveSourceAttempt(attempt knowl.SourceAttempt) {
	observer.events <- attempt
}

type isolatingRuntimeSourceAdapter struct {
	good          app.SourceAdapter
	failingSource domain.SourceID
	failure       error
}

type acceptanceRuntimeAdapter struct {
	inner app.SourceAdapter
	mu    sync.Mutex

	fetches        map[domain.SourceID]int
	failSource     domain.SourceID
	failAfterToken bool
	failure        error
}

func newAcceptanceRuntimeAdapter(inner app.SourceAdapter) *acceptanceRuntimeAdapter {
	return &acceptanceRuntimeAdapter{inner: inner, fetches: make(map[domain.SourceID]int)}
}

func (adapter *acceptanceRuntimeAdapter) List(ctx context.Context, source domain.Source, token string) (domain.DocumentPage, error) {
	adapter.mu.Lock()
	fail := source.ID == adapter.failSource && adapter.failure != nil && (!adapter.failAfterToken || token != "")
	failure := adapter.failure
	adapter.mu.Unlock()
	if fail {
		return domain.DocumentPage{}, failure
	}
	return adapter.inner.List(ctx, source, token)
}

func (adapter *acceptanceRuntimeAdapter) Fetch(ctx context.Context, source domain.Source, ref domain.DocumentRef) (domain.Document, error) {
	adapter.mu.Lock()
	adapter.fetches[source.ID]++
	fail := source.ID == adapter.failSource && adapter.failure != nil && !adapter.failAfterToken
	failure := adapter.failure
	adapter.mu.Unlock()
	if fail {
		return domain.Document{}, failure
	}
	return adapter.inner.Fetch(ctx, source, ref)
}

func (adapter *acceptanceRuntimeAdapter) failAfterFirst(sourceID domain.SourceID, failure error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.failSource, adapter.failure, adapter.failAfterToken = sourceID, failure, true
}

func (adapter *acceptanceRuntimeAdapter) failAll(sourceID domain.SourceID, failure error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.failSource, adapter.failure, adapter.failAfterToken = sourceID, failure, false
}

func (adapter *acceptanceRuntimeAdapter) clearFailure() {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.failSource, adapter.failure, adapter.failAfterToken = "", nil, false
}

func (adapter *acceptanceRuntimeAdapter) resetFetches() {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.fetches = make(map[domain.SourceID]int)
}

func (adapter *acceptanceRuntimeAdapter) fetchCount(sourceID domain.SourceID) int {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return adapter.fetches[sourceID]
}

func (adapter *acceptanceRuntimeAdapter) fetchSnapshot() map[domain.SourceID]int {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	snapshot := make(map[domain.SourceID]int, len(adapter.fetches))
	for sourceID, count := range adapter.fetches {
		snapshot[sourceID] = count
	}
	return snapshot
}

func queryRuntimeSource(t *testing.T, host *knowl.Host, scope domain.ScopeRef, query string, sources []domain.SourceID) []domain.PageReference {
	t.Helper()
	result, err := host.Query().Query(context.Background(), scope, query, domain.ReadLimits{Pages: 16}, sources)
	if err != nil {
		t.Fatalf("query %q with sources %v: %v", query, sources, err)
	}
	return result.Pages
}

func runtimeSourceInventory(t *testing.T, host *knowl.Host, scope domain.ScopeRef, sourceID domain.SourceID) map[string]string {
	t.Helper()
	entries, err := host.Workspace().SourceDigests(context.Background(), scope, sourceID, 64)
	if err != nil {
		t.Fatalf("source inventory %s: %v", sourceID, err)
	}
	inventory := make(map[string]string, len(entries))
	for _, entry := range entries {
		inventory[entry.Path] = entry.Digest
	}
	return inventory
}

func runtimeRawDocumentContent(t *testing.T, host *knowl.Host, scope domain.ScopeRef, sourceID domain.SourceID, documentID domain.DocumentID) ([]byte, bool) {
	t.Helper()
	inspection, err := host.Workspace().Inspect(context.Background(), scope)
	if err != nil {
		t.Fatalf("inspect raw sources: %v", err)
	}
	var contents []byte
	found := false
	for _, raw := range inspection.RawSources {
		document := raw.Source.SourceDocument
		if document.SourceID != sourceID || document.DocumentID != documentID {
			continue
		}
		content, readErr := host.Workspace().ReadSource(context.Background(), raw.Source, domain.ReadLimits{})
		if readErr != nil {
			t.Fatalf("read raw source %s/%s: %v", sourceID, documentID, readErr)
		}
		contents = append(contents, content...)
		contents = append(contents, '\n')
		found = true
	}
	return contents, found
}

func (adapter *isolatingRuntimeSourceAdapter) List(ctx context.Context, source domain.Source, token string) (domain.DocumentPage, error) {
	if source.ID == adapter.failingSource {
		return domain.DocumentPage{}, adapter.failure
	}
	return adapter.good.List(ctx, source, token)
}

func (adapter *isolatingRuntimeSourceAdapter) Fetch(ctx context.Context, source domain.Source, ref domain.DocumentRef) (domain.Document, error) {
	if source.ID == adapter.failingSource {
		return domain.Document{}, adapter.failure
	}
	return adapter.good.Fetch(ctx, source, ref)
}

type blockingRuntimeSourceAdapter struct {
	once    sync.Once
	started chan struct{}
}

func (adapter *blockingRuntimeSourceAdapter) List(ctx context.Context, _ domain.Source, _ string) (domain.DocumentPage, error) {
	adapter.once.Do(func() { close(adapter.started) })
	<-ctx.Done()
	return domain.DocumentPage{}, ctx.Err()
}

func (*blockingRuntimeSourceAdapter) Fetch(context.Context, domain.Source, domain.DocumentRef) (domain.Document, error) {
	return domain.Document{}, errors.New("unexpected fetch")
}

func (adapter *runtimeSourceAdapter) List(_ context.Context, _ domain.Source, token string) (domain.DocumentPage, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.lists++
	if token != "" {
		return domain.DocumentPage{}, app.ErrSourceInvalid
	}
	return domain.DocumentPage{Documents: []domain.DocumentRef{{ExternalID: "custom.md", Path: "custom.md", Revision: runtimeSourceDigest([]byte("# Overridebeacon\n"))}}}, nil
}

func (adapter *runtimeSourceAdapter) Fetch(_ context.Context, _ domain.Source, ref domain.DocumentRef) (domain.Document, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.fetches++
	return domain.Document{
		DocumentRef: ref, Title: "Overridebeacon", URI: "https://example.test/custom.md",
		MediaType: "text/markdown", Content: []byte("# Overridebeacon\n"),
	}, nil
}

func (adapter *runtimeSourceAdapter) calls() (int, int) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return adapter.lists, adapter.fetches
}

func runtimeSourceDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
