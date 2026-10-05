package knowl

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"iter"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/normahq/runtime/v2/agentfactory"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
)

const (
	slotProviderID             = "isolated"
	lifecycleProviderID        = "lifecycle"
	lifecycleMarkdownMediaType = "text/markdown"
)

func TestHierarchyAdmissionPrecedesReservation(t *testing.T) {
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.Workers = 2
	host, err := New(t.Context(), Options{Config: config, RuntimeFactory: &slotRuntimeFactory{}, ProviderID: slotProviderID})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Close() }()
	first, err := host.slots.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()
	second, err := host.slots.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer second.release()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err = host.ReconcileHierarchy(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("occupied hierarchy admission=%v", err)
	}
	ready, err := host.operations.ResumeReady(t.Context(), config.Scope, 10)
	if err != nil || len(ready) != 0 {
		t.Fatalf("reserved hierarchy before admission=%v %v", ready, err)
	}
}

func TestStopRetainsDirectRuntimeUntilItExits(t *testing.T) {
	for _, hierarchy := range []bool{false, true} {
		t.Run(fmt.Sprintf("hierarchy%t", hierarchy), func(t *testing.T) {
			config := DefaultConfig()
			config.Workspace = t.TempDir()
			factory := &lifecycleRuntimeFactory{entered: make(chan struct{}, 1), canceled: make(chan struct{}, 1), release: make(chan struct{})}
			options := Options{Config: config, RuntimeFactory: factory, ProviderID: lifecycleProviderID}
			if !hierarchy {
				options = Options{Config: config, Maintainer: &lifecycleLintMaintainer{factory: factory}}
			}
			host, err := New(t.Context(), options)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			finished := make(chan error, 1)
			go func() {
				if hierarchy {
					_, runErr := host.ReconcileHierarchy(ctx)
					finished <- runErr
				} else {
					_, runErr := host.Lint().Lint(ctx, config.Scope)
					finished <- runErr
				}
			}()
			released := false
			defer func() {
				cancel()
				if !released {
					close(factory.release)
				}
				_ = host.Stop(context.Background())
			}()
			select {
			case <-factory.entered:
			case <-ctx.Done():
				t.Fatal("direct call did not enter runtime")
			}
			stopCtx, stopCancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			stopped := make(chan error, 1)
			go func() { stopped <- host.Stop(stopCtx) }()
			select {
			case err = <-stopped:
			case <-time.After(250 * time.Millisecond):
				close(factory.release)
				released = true
				<-finished
				<-stopped
				stopCancel()
				t.Fatal("Stop blocked closing an untracked live direct runtime")
			}
			stopCancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("stop failed to account for direct owner=%v", err)
			}
			if factory.closes.Load() != 0 {
				t.Fatal("closed live provider")
			}
			select {
			case <-factory.canceled:
			case <-ctx.Done():
				t.Fatal("shutdown deadline did not cancel direct runtime")
			}
			if _, err = host.ReconcileHierarchy(ctx); !errors.Is(err, ErrHostClosed) {
				t.Fatalf("post-stop direct admission=%v", err)
			}
			close(factory.release)
			released = true
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("direct call did not exit after release")
			}
			if err = host.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			if factory.builds.Load() != 1 || factory.closes.Load() != 1 {
				t.Fatalf("runtime ownership builds%d closes%d", factory.builds.Load(), factory.closes.Load())
			}
		})
	}
}

type lifecycleRuntimeFactory struct {
	builds, closes             atomic.Int32
	entered, canceled, release chan struct{}
}

func (factory *lifecycleRuntimeFactory) Build(context.Context, agentfactory.BuildRequest) (adkagent.Agent, error) {
	factory.builds.Add(1)
	agent, err := adkagent.New(adkagent.Config{Name: lifecycleProviderID, Run: func(ctx adkagent.InvocationContext) iter.Seq2[*session.Event, error] {
		return func(yield func(*session.Event, error) bool) {
			factory.entered <- struct{}{}
			select {
			case <-ctx.Done():
				factory.canceled <- struct{}{}
				<-factory.release
				yield(nil, ctx.Err())
			case <-factory.release:
				yield(nil, app.ErrMaintainerUnavailable)
			}
		}
	}})
	if err != nil {
		return nil, err
	}
	return &slotOwnedAgent{Agent: agent, closes: &factory.closes}, nil
}

type lifecycleLintMaintainer struct{ factory *lifecycleRuntimeFactory }

func (maintainer *lifecycleLintMaintainer) Plan(ctx context.Context, _ domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	maintainer.factory.builds.Add(1)
	maintainer.factory.entered <- struct{}{}
	select {
	case <-ctx.Done():
		maintainer.factory.canceled <- struct{}{}
		<-maintainer.factory.release
		return domain.ModelEditPlan{}, ctx.Err()
	case <-maintainer.factory.release:
		return domain.ModelEditPlan{}, app.ErrMaintainerUnavailable
	}
}
func (maintainer *lifecycleLintMaintainer) Close() error {
	maintainer.factory.closes.Add(1)
	return nil
}

func TestHostMixedEntriesShareOwnersAndShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.Workers = 2
	factory := &lifecycleRuntimeFactory{entered: make(chan struct{}, 2), canceled: make(chan struct{}, 2), release: make(chan struct{})}
	host, err := New(ctx, Options{Config: config, RuntimeFactory: factory, ProviderID: lifecycleProviderID})
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		cancel()
		if !released {
			close(factory.release)
		}
		_ = host.Stop(context.Background())
	}()
	submit := func(id string) domain.OperationID {
		t.Helper()
		content := []byte("# Notes\n\nDurable technical evidence.")
		sum := sha256.Sum256(content)
		result, submitErr := host.service.Submit(ctx, domain.SourceEnvelope{Scope: config.Scope, Source: domain.SourceRef{Adapter: "mixed-entry", ID: id}, Version: domain.SourceVersion{Version: "1", Digest: fmt.Sprintf("%x", sum)}, Content: content, MediaType: lifecycleMarkdownMediaType})
		if submitErr != nil {
			t.Fatal(submitErr)
		}
		return result.Operation.ID
	}
	submit("first")
	if err = host.StartOperationWorker(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-factory.entered:
	case <-ctx.Done():
		t.Fatal("background owner did not enter")
	}
	hierarchyDone := make(chan error, 1)
	go func() { _, runErr := host.ReconcileHierarchy(ctx); hierarchyDone <- runErr }()
	select {
	case <-factory.entered:
	case <-ctx.Done():
		t.Fatal("direct hierarchy did not use second owner")
	}
	pending := submit("pending")
	host.scheduler.Wake(pending)
	drainDone := make(chan error, 1)
	go func() { _, runErr := host.Drain(ctx); drainDone <- runErr }()
	lintDone := make(chan error, 1)
	go func() { _, runErr := host.Lint().Lint(ctx, config.Scope); lintDone <- runErr }()
	stopCtx, stopCancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	err = host.Stop(stopCtx)
	stopCancel()
	if !errors.Is(err, context.DeadlineExceeded) || factory.builds.Load() != 2 || factory.closes.Load() != 0 {
		t.Fatalf("shared ownership stop%v builds%d closes%d", err, factory.builds.Load(), factory.closes.Load())
	}
	op, err := host.operations.Operation(ctx, config.Scope, pending)
	if err != nil || op.WorkAttempt != 0 {
		t.Fatalf("pending work preclaimed=%#v %v", op, err)
	}
	for range 2 {
		select {
		case <-factory.canceled:
		case <-ctx.Done():
			t.Fatal("shutdown did not cancel both owners")
		}
	}
	close(factory.release)
	released = true
	for _, finished := range []<-chan error{hierarchyDone, drainDone, lintDone} {
		select {
		case <-finished:
		case <-ctx.Done():
			t.Fatal("mixed caller did not exit")
		}
	}
	if err = host.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if factory.builds.Load() != 2 || factory.closes.Load() != 2 {
		t.Fatalf("final owner counts builds%d closes%d", factory.builds.Load(), factory.closes.Load())
	}
}
