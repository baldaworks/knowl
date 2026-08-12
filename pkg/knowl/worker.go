package knowl

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"
)

var (
	errWorkerStopped = errors.New("knowl worker is stopped")
	errWorkerFull    = errors.New("knowl worker queue is full")
)

type workItem struct {
	fn func(context.Context) error
}

type worker struct {
	queue    chan workItem
	done     chan struct{}
	mu       sync.Mutex
	submitMu sync.RWMutex
	cancel   context.CancelFunc
	started  bool
	stopped  bool
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func newWorker(queueSize int) *worker {
	if queueSize <= 0 {
		queueSize = 1
	}
	return &worker{queue: make(chan workItem, queueSize), done: make(chan struct{})}
}

func (worker *worker) start(ctx context.Context) error {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.started || worker.stopped {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	worker.started = true
	worker.wg.Add(1)
	workerCtx, cancel := context.WithCancel(ctx)
	worker.cancel = cancel
	go worker.run(workerCtx)
	return nil
}

// submit enqueues work for host-owned execution without waiting for completion.
func (worker *worker) submit(ctx context.Context, fn func(context.Context) error) error {
	return worker.enqueue(ctx, workItem{fn: fn})
}

func (worker *worker) enqueue(ctx context.Context, item workItem) error {
	if item.fn == nil {
		return fmt.Errorf("worker function is required: %w", errWorkerFull)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	worker.submitMu.RLock()
	worker.mu.Lock()
	started, stopped := worker.started, worker.stopped
	worker.mu.Unlock()
	if !started || stopped {
		worker.submitMu.RUnlock()
		return errWorkerStopped
	}
	select {
	case worker.queue <- item:
		worker.submitMu.RUnlock()
	case <-ctx.Done():
		worker.submitMu.RUnlock()
		return ctx.Err()
	default:
		worker.submitMu.RUnlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-worker.done:
			return errWorkerStopped
		default:
			return errWorkerFull
		}
	}
	return nil
}

func (worker *worker) stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	worker.submitMu.Lock()
	worker.mu.Lock()
	if !worker.stopped {
		worker.stopped = true
		if worker.cancel != nil {
			worker.cancel()
		}
	}
	worker.mu.Unlock()
	worker.signalStopped()
	worker.submitMu.Unlock()
	finished := make(chan struct{})
	go func() {
		worker.wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (worker *worker) run(ctx context.Context) {
	defer worker.wg.Done()
	defer worker.signalStopped()
	for {
		select {
		case <-ctx.Done():
			worker.drain()
			return
		case item := <-worker.queue:
			if item.fn == nil {
				continue
			}
			itemContext, cancel := context.WithCancel(ctx)
			if err := item.fn(itemContext); err != nil {
				log.Error().Err(err).Msg("knowl background operation failed")
				cancel()
				continue
			}
			cancel()
		}
	}
}

func (worker *worker) signalStopped() {
	worker.stopOnce.Do(func() { close(worker.done) })
}

func (worker *worker) drain() {
	for {
		select {
		case <-worker.queue:
		default:
			return
		}
	}
}
