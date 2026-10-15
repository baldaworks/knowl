package knowl

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

type recordingSourceLifecycleStore struct {
	authority domain.SourceLifecycleAuthority
	request   app.SourceLifecycleReconcileRequest
	err       error
}

const (
	runtimeLifecycleGitRef   = "main"
	runtimeLifecycleSourceID = domain.SourceID("same")
)

func (store *recordingSourceLifecycleStore) ReconcileSourceLifecycles(_ context.Context, request app.SourceLifecycleReconcileRequest) error {
	store.request = request
	return store.err
}

func (store *recordingSourceLifecycleStore) SourceLifecycleAuthority(context.Context, domain.ScopeRef) (domain.SourceLifecycleAuthority, error) {
	return store.authority, nil
}

func (*recordingSourceLifecycleStore) SourceLifecycle(context.Context, domain.ScopeRef, domain.SourceOwner) (domain.SourceLifecycle, error) {
	return domain.SourceLifecycle{}, app.ErrSourceLifecycleUnowned
}

func (*recordingSourceLifecycleStore) SourceLifecycles(context.Context, domain.ScopeRef, app.SourceLifecycleListOptions) ([]domain.SourceLifecycle, error) {
	return nil, nil
}

func (*recordingSourceLifecycleStore) MarkSourceLifecycleReady(context.Context, app.SourceLifecycleReadyUpdate) (domain.SourceLifecycle, error) {
	return domain.SourceLifecycle{}, nil
}

func (*recordingSourceLifecycleStore) UpdateSourceRetirement(context.Context, app.SourceRetirementUpdate) (domain.SourceLifecycle, error) {
	return domain.SourceLifecycle{}, nil
}

func (*recordingSourceLifecycleStore) FinalizeSourceRetirement(context.Context, app.SourceRetirementFinalization) (domain.SourceLifecycle, error) {
	return domain.SourceLifecycle{}, nil
}

func TestReconcileConfiguredSourceLifecyclesUsesExactBoundedInventory(t *testing.T) {
	t.Parallel()
	at := time.Unix(1_900_000_000, 0).UTC()
	scope := domain.ScopeRef("runtime-lifecycle")
	store := &recordingSourceLifecycleStore{authority: domain.SourceLifecycleAuthority{
		Scope: scope, Generation: strings.Repeat("9", 64), Epoch: 7, UpdatedAt: at.Add(-time.Second),
	}}
	sources := []domain.Source{
		{ID: "z-disabled", Type: domain.SourceTypeFilesystem, Enabled: false, Config: domain.SourceConfig{Filesystem: &domain.FilesystemSourceConfig{Root: "/source/z"}}, ConfigDigest: strings.Repeat("b", 64)},
		{ID: runtimeLifecycleSourceID, Type: domain.SourceTypeGit, Enabled: true, Config: domain.SourceConfig{Git: &domain.GitSourceConfig{Remote: "https://example.test/wiki.git", Ref: runtimeLifecycleGitRef}}, ConfigDigest: strings.Repeat("a", 64)},
	}

	err := reconcileConfiguredSourceLifecycles(context.Background(), store, scope, sources, at)
	if err != nil {
		t.Fatalf("reconcileConfiguredSourceLifecycles() error = %v", err)
	}
	want := []app.DesiredSourceOwner{
		{Owner: domain.SourceOwner{Adapter: domain.SourceAdapterGit, SourceID: runtimeLifecycleSourceID}, ConfigDigest: strings.Repeat("a", 64)},
		{Owner: domain.SourceOwner{Adapter: domain.SourceAdapterFilesystem, SourceID: "z-disabled"}, ConfigDigest: strings.Repeat("b", 64)},
	}
	if store.request.Scope != scope || store.request.ExpectedAuthorityEpoch != 7 || !store.request.UpdatedAt.Equal(at) ||
		store.request.AuthorityGeneration != app.SourceLifecycleGeneration(want) || !reflect.DeepEqual(store.request.Desired, want) {
		t.Fatalf("reconcile request = %#v, want desired %#v at epoch 7", store.request, want)
	}
}

func TestReconcileConfiguredSourceLifecyclesFailsClosed(t *testing.T) {
	t.Parallel()
	cause := errors.New("lifecycle unavailable")
	store := &recordingSourceLifecycleStore{err: cause}
	err := reconcileConfiguredSourceLifecycles(context.Background(), store, "runtime-lifecycle", nil, time.Unix(1_900_000_000, 0).UTC())
	if !errors.Is(err, cause) {
		t.Fatalf("reconcileConfiguredSourceLifecycles() error = %v, want durable cause", err)
	}
}

func TestNewPersistsAdapterReplacementBeforeSourceRecovery(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	storePath := filepath.Join(workspace, "state.sqlite")
	oldConfig := DefaultConfig()
	oldConfig.Workspace = workspace
	oldConfig.StorePath = storePath
	oldConfig.Sources = []domain.Source{{
		ID: runtimeLifecycleSourceID, Type: domain.SourceTypeFilesystem, Enabled: true,
		Config: domain.SourceConfig{Filesystem: &domain.FilesystemSourceConfig{Root: t.TempDir()}},
	}}
	normalizedOld, err := oldConfig.normalized()
	if err != nil {
		t.Fatalf("normalize old config: %v", err)
	}
	seed, err := sqlite.Open(ctx, storePath)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	at := time.Now().UTC().Add(-time.Minute)
	if _, _, err := seed.BeginSync(ctx, app.BeginSyncRequest{Run: domain.SyncRun{
		ID: "old-scan", Scope: normalizedOld.Scope, SourceID: runtimeLifecycleSourceID, ConfigDigest: normalizedOld.Sources[0].ConfigDigest,
		Status: domain.SyncStatusScanning, StartedAt: at, UpdatedAt: at,
	}, Type: domain.SourceTypeFilesystem}); err != nil {
		t.Fatalf("seed old source: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	newConfig := oldConfig
	newConfig.Sources = []domain.Source{{
		ID: runtimeLifecycleSourceID, Type: domain.SourceTypeGit, Enabled: true,
		Config: domain.SourceConfig{Git: &domain.GitSourceConfig{Remote: "https://example.test/wiki.git", Ref: runtimeLifecycleGitRef}},
	}}
	host, err := New(ctx, Options{Config: newConfig, Maintainer: provider.Fixture{}})
	if err != nil {
		t.Fatalf("New() replacement host error = %v", err)
	}
	t.Cleanup(func() { _ = host.Stop(context.Background()) })
	peer, err := sqlite.Open(ctx, storePath)
	if err != nil {
		t.Fatalf("open lifecycle peer: %v", err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	oldState, err := peer.SourceLifecycle(ctx, normalizedOld.Scope, domain.SourceOwner{Adapter: domain.SourceAdapterFilesystem, SourceID: runtimeLifecycleSourceID})
	if err != nil {
		t.Fatalf("read retiring owner: %v", err)
	}
	newState, err := peer.SourceLifecycle(ctx, normalizedOld.Scope, domain.SourceOwner{Adapter: domain.SourceAdapterGit, SourceID: runtimeLifecycleSourceID})
	if err != nil {
		t.Fatalf("read active owner: %v", err)
	}
	if oldState.State != domain.SourceLifecycleRetiring || newState.State != domain.SourceLifecycleActive || newState.Ready ||
		oldState.RetirementGeneration != newState.AuthorityGeneration {
		t.Fatalf("replacement lifecycle = old %#v, new %#v", oldState, newState)
	}
	retirement, err := host.Operator().SourceRetirement(ctx, oldState.Owner)
	if err != nil {
		t.Fatalf("runtime retirement status: %v", err)
	}
	if retirement.Owner != oldState.Owner || retirement.State != domain.SourceLifecycleRetiring ||
		retirement.RetirementGeneration != oldState.RetirementGeneration || retirement.Readiness.Ready {
		t.Fatalf("runtime retirement status = %#v", retirement)
	}
}
