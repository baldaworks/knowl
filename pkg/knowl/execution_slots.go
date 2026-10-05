package knowl

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	runtimeprovider "github.com/baldaworks/knowl/pkg/knowl/provider"
	"github.com/normahq/runtime/v2/agentfactory"
	adkagent "google.golang.org/adk/v2/agent"
)

var errExecutionInUse = errors.New("maintenance execution is still in use")

type executionSlot struct {
	maintainer app.Maintainer
	closer     io.Closer
	source     *app.IngestService
	hierarchy  *app.HierarchyService
	closed     bool
}

type executionSlots struct {
	all       []*executionSlot
	available chan *executionSlot
	stopping  chan struct{}
	lifetime  context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	stopped   bool
	active    int
	idle      chan struct{}
	closeMu   sync.Mutex
}

type executionUse struct {
	slot             *executionSlot
	ctx              context.Context
	pool             *executionSlots
	cancel           context.CancelFunc
	stopCancellation func() bool
	once             sync.Once
}

func newExecutionSlots(owners []*executionSlot) *executionSlots {
	lifetime, cancel := context.WithCancel(context.Background())
	idle := make(chan struct{})
	close(idle)
	pool := &executionSlots{all: owners, available: make(chan *executionSlot, len(owners)), stopping: make(chan struct{}), lifetime: lifetime, cancel: cancel, idle: idle}
	for _, owner := range owners {
		pool.available <- owner
	}
	return pool
}

func (pool *executionSlots) acquire(ctx context.Context) (*executionUse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var slot *executionSlot
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-pool.stopping:
		return nil, ErrHostClosed
	case slot = <-pool.available:
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.stopped {
		return nil, ErrHostClosed
	}
	if err := ctx.Err(); err != nil {
		pool.available <- slot
		return nil, err
	}
	if pool.active == 0 {
		pool.idle = make(chan struct{})
	}
	pool.active++
	useCtx, cancel := context.WithCancel(ctx)
	return &executionUse{slot: slot, ctx: useCtx, pool: pool, cancel: cancel, stopCancellation: context.AfterFunc(pool.lifetime, cancel)}, nil
}

func (use *executionUse) release() {
	use.once.Do(func() {
		use.stopCancellation()
		use.cancel()
		pool := use.pool
		pool.mu.Lock()
		defer pool.mu.Unlock()
		pool.active--
		if !pool.stopped {
			pool.available <- use.slot
		}
		if pool.active == 0 {
			close(pool.idle)
		}
	})
}

func (pool *executionSlots) stopAdmission() {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if !pool.stopped {
		pool.stopped = true
		close(pool.stopping)
	}
}

func (pool *executionSlots) wait(ctx context.Context) error {
	pool.mu.Lock()
	idle := pool.idle
	pool.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close rejects live users and retains ownership of any resource whose close failed.
func (pool *executionSlots) Close() error {
	pool.closeMu.Lock()
	defer pool.closeMu.Unlock()
	pool.stopAdmission()
	pool.mu.Lock()
	active := pool.active
	pool.mu.Unlock()
	if active != 0 {
		return errExecutionInUse
	}
	pool.cancel()
	var failures []error
	for _, slot := range pool.all {
		if slot.closed {
			continue
		}
		if slot.closer != nil {
			if err := slot.closer.Close(); err != nil {
				failures = append(failures, err)
				continue
			}
		}
		slot.closed = true
	}
	return errors.Join(failures...)
}

// factory builds are short setup sections; inference owns independent agents.
type serialRuntimeFactory struct {
	runtimeprovider.RuntimeFactory
	gate chan struct{}
}

func (factory *serialRuntimeFactory) Build(ctx context.Context, request agentfactory.BuildRequest) (adkagent.Agent, error) {
	select {
	case factory.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-factory.gate }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return factory.RuntimeFactory.Build(ctx, request)
}
func (factory *serialRuntimeFactory) ValidateAgent(id string) error {
	if validator, ok := factory.RuntimeFactory.(runtimeprovider.RuntimeFactoryValidator); ok {
		return validator.ValidateAgent(id)
	}
	return nil
}

func (options Options) executionSlots(config Config) (*executionSlots, error) {
	if options.RuntimeFactory != nil {
		options.RuntimeFactory = &serialRuntimeFactory{RuntimeFactory: options.RuntimeFactory, gate: make(chan struct{}, 1)}
	}
	owners := make([]*executionSlot, 0, config.Workers)
	for range config.Workers {
		maintainer, closer, err := options.maintainer(config)
		if err != nil {
			_ = newExecutionSlots(owners).Close()
			return nil, err
		}
		owners = append(owners, &executionSlot{maintainer: maintainer, closer: closer})
	}
	return newExecutionSlots(owners), nil
}
