package knowl

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/internal/retirementcommit"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type hostRetirementLifecycle struct {
	mu        sync.Mutex
	authority knowl.SourceLifecycleAuthority
	state     knowl.SourceLifecycle
}

func (reader *hostRetirementLifecycle) SourceLifecycleAuthority(ctx context.Context, _ knowl.ScopeRef) (knowl.SourceLifecycleAuthority, error) {
	if err := ctx.Err(); err != nil {
		return knowl.SourceLifecycleAuthority{}, err
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.authority, nil
}

func (reader *hostRetirementLifecycle) SourceLifecycle(ctx context.Context, _ knowl.ScopeRef, _ knowl.SourceOwner) (knowl.SourceLifecycle, error) {
	if err := ctx.Err(); err != nil {
		return knowl.SourceLifecycle{}, err
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.state, nil
}

func (reader *hostRetirementLifecycle) changeConfig() {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.state.ConfigDigest = strings.Repeat("e", 64)
}

type hostRetirementCoordinator struct {
	gate     chan struct{}
	requests chan struct{}
}

func newHostRetirementCoordinator() *hostRetirementCoordinator {
	return &hostRetirementCoordinator{gate: make(chan struct{}, 1), requests: make(chan struct{}, 8)}
}

func (coordinator *hostRetirementCoordinator) Acquire(ctx context.Context) (func(), error) {
	coordinator.requests <- struct{}{}
	select {
	case coordinator.gate <- struct{}{}:
		return func() { <-coordinator.gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestHostRetirementCommitterBindsCoordinatorLifecycleWorkspaceAndSerializesBothOrders(t *testing.T) {
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, staged := hostRetirementFixture()

	t.Run("identity", func(t *testing.T) {
		coordinator := newHostRetirementCoordinator()
		committer, err := newHostRetirementCommitter(coordinator, lifecycle, workspace)
		if err != nil {
			t.Fatal(err)
		}
		if committer.coordinator != coordinator || committer.lifecycles != lifecycle || committer.workspace != workspace || committer.committed == nil || committer.commit == nil {
			t.Fatalf("committer dependencies are not the exact composed instances: %#v", committer)
		}
		var _ app.RetirementCommitter = committer
	})

	t.Run("commit wins", func(t *testing.T) {
		lifecycle, staged = hostRetirementFixture()
		coordinator := newHostRetirementCoordinator()
		writerEntered := make(chan struct{})
		releaseWriter := make(chan struct{})
		committer := &hostRetirementCommitter{
			coordinator: coordinator, lifecycles: lifecycle, workspace: workspace,
			committed: noHostRetirementReceipt,
			commit: func(_ context.Context, request retirementcommit.Request) (knowl.ContentCommit, error) {
				if request.Stage().OperationID != staged.OperationID {
					t.Fatalf("writer stage = %#v", request.Stage())
				}
				close(writerEntered)
				<-releaseWriter
				return knowl.ContentCommit{OperationID: staged.OperationID}, nil
			},
		}
		result := make(chan error, 1)
		go func() {
			_, commitErr := runHostRetirementApply(t.Context(), committer, staged, nil)
			result <- commitErr
		}()
		<-coordinator.requests
		<-writerEntered
		changed := make(chan struct{})
		go func() {
			release, acquireErr := coordinator.Acquire(t.Context())
			if acquireErr == nil {
				lifecycle.changeConfig()
				release()
			}
			close(changed)
		}()
		<-coordinator.requests
		select {
		case <-changed:
			t.Fatal("lifecycle mutation passed the live commit fence")
		default:
		}
		close(releaseWriter)
		if commitErr := <-result; commitErr != nil {
			t.Fatal(commitErr)
		}
		<-changed
	})

	t.Run("lifecycle change wins", func(t *testing.T) {
		lifecycle, staged = hostRetirementFixture()
		coordinator := newHostRetirementCoordinator()
		release, err := coordinator.Acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		<-coordinator.requests
		writerCalled := false
		committer := &hostRetirementCommitter{
			coordinator: coordinator, lifecycles: lifecycle, workspace: workspace,
			committed: noHostRetirementReceipt,
			commit: func(context.Context, retirementcommit.Request) (knowl.ContentCommit, error) {
				writerCalled = true
				return knowl.ContentCommit{}, nil
			},
		}
		result := make(chan error, 1)
		go func() {
			_, commitErr := runHostRetirementApply(t.Context(), committer, staged, nil)
			result <- commitErr
		}()
		<-coordinator.requests
		lifecycle.changeConfig()
		release()
		if commitErr := <-result; !errors.Is(commitErr, app.ErrRetirementLifecycleConflict) {
			t.Fatalf("RunRetirementApply() error = %v, want lifecycle conflict", commitErr)
		}
		if writerCalled {
			t.Fatal("stale lifecycle reached canonical writer")
		}
	})

	t.Run("receipt replay holds fence without lifecycle revalidation", func(t *testing.T) {
		lifecycle, staged = hostRetirementFixture()
		lifecycle.changeConfig()
		coordinator := newHostRetirementCoordinator()
		commit := knowl.ContentCommit{OperationID: staged.OperationID, Generation: staged.Digest}
		writerCalled := false
		completeEntered := make(chan struct{})
		completeRelease := make(chan struct{})
		committer := &hostRetirementCommitter{
			coordinator: coordinator, lifecycles: lifecycle, workspace: workspace,
			committed: func(context.Context, knowl.StagedChange) (knowl.ContentCommit, bool, error) {
				return commit, true, nil
			},
			commit: func(context.Context, retirementcommit.Request) (knowl.ContentCommit, error) {
				writerCalled = true
				return knowl.ContentCommit{}, nil
			},
		}
		result := make(chan error, 1)
		go func() {
			_, applyErr := runHostRetirementApply(t.Context(), committer, staged, func(_ context.Context, got knowl.ContentCommit) error {
				if got.OperationID != commit.OperationID || got.Generation != commit.Generation {
					t.Errorf("completion commit = %#v, want %#v", got, commit)
				}
				close(completeEntered)
				<-completeRelease
				return nil
			})
			result <- applyErr
		}()
		<-completeEntered
		waitCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		release, acquireErr := coordinator.Acquire(waitCtx)
		cancel()
		if release != nil {
			release()
		}
		close(completeRelease)
		if applyErr := <-result; applyErr != nil {
			t.Fatal(applyErr)
		}
		if !errors.Is(acquireErr, context.DeadlineExceeded) {
			t.Fatalf("competing publisher acquired receipt replay fence: %v", acquireErr)
		}
		if writerCalled {
			t.Fatal("receipt replay invoked canonical writer")
		}
	})
}

func TestHostRetirementCommitterReleasesFenceAfterCancellationReaderAndWriterErrors(t *testing.T) {
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, staged := hostRetirementFixture()
	writerErr := errors.New("writer failed")
	prepareErr := errors.New("prepare failed")
	completeErr := errors.New("completion failed")
	tests := []struct {
		name     string
		ctx      func() context.Context
		mutate   bool
		prepare  func(context.Context) (bool, error)
		writer   func(context.Context, retirementcommit.Request) (knowl.ContentCommit, error)
		complete func(context.Context, knowl.ContentCommit) error
		want     error
	}{
		{name: "cancellation", ctx: func() context.Context { ctx, cancel := context.WithCancel(t.Context()); cancel(); return ctx }, writer: workspace.CommitRetirementBound, want: context.Canceled},
		{name: "prepare", ctx: t.Context, prepare: func(context.Context) (bool, error) { return false, prepareErr }, writer: workspace.CommitRetirementBound, want: prepareErr},
		{name: "reader", ctx: t.Context, mutate: true, writer: workspace.CommitRetirementBound, want: app.ErrRetirementLifecycleConflict},
		{name: "writer", ctx: t.Context, writer: func(context.Context, retirementcommit.Request) (knowl.ContentCommit, error) {
			return knowl.ContentCommit{}, writerErr
		}, want: writerErr},
		{name: "completion", ctx: t.Context, writer: func(_ context.Context, request retirementcommit.Request) (knowl.ContentCommit, error) {
			return knowl.ContentCommit{OperationID: request.Stage().OperationID}, nil
		}, complete: func(context.Context, knowl.ContentCommit) error { return completeErr }, want: completeErr},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lifecycle, staged = hostRetirementFixture()
			coordinator := newHostRetirementCoordinator()
			if test.mutate {
				lifecycle.changeConfig()
			}
			committer := &hostRetirementCommitter{coordinator: coordinator, lifecycles: lifecycle, workspace: workspace, committed: noHostRetirementReceipt, commit: test.writer}
			prepare := test.prepare
			if prepare == nil {
				prepare = hostRetirementPrepare
			}
			complete := test.complete
			if complete == nil {
				complete = hostRetirementComplete
			}
			_, commitErr := committer.RunRetirementApply(test.ctx(), staged, prepare, complete)
			if !errors.Is(commitErr, test.want) {
				t.Fatalf("RunRetirementApply() error = %v, want %v", commitErr, test.want)
			}
			release, acquireErr := coordinator.Acquire(t.Context())
			if acquireErr != nil {
				t.Fatalf("coordinator remained held: %v", acquireErr)
			}
			release()
		})
	}
}

func TestHostRetirementCommitterCanceledCompletionReleasesFence(t *testing.T) {
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, staged := hostRetirementFixture()
	coordinator := newHostRetirementCoordinator()
	committer := &hostRetirementCommitter{
		coordinator: coordinator, lifecycles: lifecycle, workspace: workspace,
		committed: noHostRetirementReceipt,
		commit: func(_ context.Context, request retirementcommit.Request) (knowl.ContentCommit, error) {
			return knowl.ContentCommit{OperationID: request.Stage().OperationID}, nil
		},
	}
	ctx, cancel := context.WithCancel(t.Context())
	completeEntered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, applyErr := runHostRetirementApply(ctx, committer, staged, func(ctx context.Context, _ knowl.ContentCommit) error {
			close(completeEntered)
			<-ctx.Done()
			return ctx.Err()
		})
		done <- applyErr
	}()
	<-completeEntered
	cancel()
	if applyErr := <-done; !errors.Is(applyErr, context.Canceled) {
		t.Fatalf("RunRetirementApply() error = %v, want cancellation", applyErr)
	}
	release, acquireErr := coordinator.Acquire(t.Context())
	if acquireErr != nil {
		t.Fatalf("coordinator remained held after canceled completion: %v", acquireErr)
	}
	release()
}

func TestHostRetirementCommitterAcquiresCoordinatorOnce(t *testing.T) {
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, staged := hostRetirementFixture()
	coordinator := newHostRetirementCoordinator()
	committer := &hostRetirementCommitter{
		coordinator: coordinator, lifecycles: lifecycle, workspace: workspace,
		committed: noHostRetirementReceipt,
		commit: func(_ context.Context, request retirementcommit.Request) (knowl.ContentCommit, error) {
			return knowl.ContentCommit{OperationID: request.Stage().OperationID}, nil
		},
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := runHostRetirementApply(ctx, committer, staged, nil); err != nil {
		t.Fatalf("RunRetirementApply() error = %v", err)
	}
	if requests := len(coordinator.requests); requests != 1 {
		t.Fatalf("coordinator acquisitions = %d, want 1", requests)
	}
}

func noHostRetirementReceipt(context.Context, knowl.StagedChange) (knowl.ContentCommit, bool, error) {
	return knowl.ContentCommit{}, false, nil
}

func hostRetirementPrepare(context.Context) (bool, error) { return true, nil }

func hostRetirementComplete(context.Context, knowl.ContentCommit) error { return nil }

func runHostRetirementApply(
	ctx context.Context,
	committer *hostRetirementCommitter,
	staged knowl.StagedChange,
	complete func(context.Context, knowl.ContentCommit) error,
) (knowl.ContentCommit, error) {
	if complete == nil {
		complete = hostRetirementComplete
	}
	return committer.RunRetirementApply(ctx, staged, hostRetirementPrepare, complete)
}

func hostRetirementFixture() (*hostRetirementLifecycle, knowl.StagedChange) {
	scope := knowl.ScopeRef("host-retirement")
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: "legacy"}
	now := time.Now().UTC()
	authority := knowl.SourceLifecycleAuthority{Scope: scope, Generation: strings.Repeat("a", 64), Epoch: 7, UpdatedAt: now}
	state := knowl.SourceLifecycle{
		Scope: scope, Owner: owner, State: knowl.SourceLifecycleRetiring, ConfigDigest: strings.Repeat("b", 64),
		AuthorityGeneration: authority.Generation, AuthorityEpoch: authority.Epoch,
		ActivatedGeneration: strings.Repeat("c", 64), ActivatedEpoch: 2, ActivatedAt: now.Add(-2 * time.Hour),
		RetirementGeneration: strings.Repeat("d", 64), RetirementEpoch: 6, RetiringAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	staged := knowl.StagedChange{OperationID: "retire-one", Digest: strings.Repeat("f", 64), Retirement: &knowl.RetirementPlanContext{
		Scope: scope, PageID: "concepts/one", PagePath: "wiki/concepts/one.md", PageDigest: strings.Repeat("1", 64),
		AuthorityGeneration: authority.Generation, AuthorityEpoch: authority.Epoch,
		Owners: []knowl.RetirementOwnerAuthority{{Owner: owner, ConfigDigest: state.ConfigDigest, RetirementGeneration: state.RetirementGeneration, RetirementEpoch: state.RetirementEpoch}},
	}}
	return &hostRetirementLifecycle{authority: authority, state: state}, staged
}
