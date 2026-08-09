package knowl

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrWorkerStopped = errors.New("knowl worker is stopped")
	ErrWorkerFull    = errors.New("knowl worker queue is full")
)

type workItem struct {
	ctx    context.Context
	fn     func(context.Context) error
	result chan error
}

// Worker serializes bounded local write work and never grows an unbounded queue.
type Worker struct {
	queue    chan workItem
	done     chan struct{}
	mu       sync.Mutex
	submitMu sync.RWMutex
	cancel   context.CancelFunc
	start    bool
	stop     bool
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewWorker constructs a single-consumer bounded worker.
func NewWorker(queueSize int) *Worker {
	if queueSize <= 0 {
		queueSize = 1
	}
	return &Worker{queue: make(chan workItem, queueSize), done: make(chan struct{})}
}

// Start launches the worker. Calling Start more than once is harmless.
func (worker *Worker) Start(ctx context.Context) error {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.start || worker.stop {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	worker.start = true
	worker.wg.Add(1)
	workerCtx, cancel := context.WithCancel(ctx)
	worker.cancel = cancel
	go worker.run(workerCtx)
	return nil
}

// Do queues one write operation and waits for its result.
func (worker *Worker) Do(ctx context.Context, fn func(context.Context) error) error {
	if fn == nil {
		return fmt.Errorf("worker function is required: %w", ErrWorkerFull)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	worker.submitMu.RLock()
	worker.mu.Lock()
	started, stopped := worker.start, worker.stop
	worker.mu.Unlock()
	if !started || stopped {
		worker.submitMu.RUnlock()
		return ErrWorkerStopped
	}
	item := workItem{ctx: ctx, fn: fn, result: make(chan error, 1)}
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
			return ErrWorkerStopped
		default:
			return ErrWorkerFull
		}
	}
	select {
	case err := <-item.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-worker.done:
		return ErrWorkerStopped
	}
}

// Stop cancels the worker and waits for queued work to finish or the context to expire.
func (worker *Worker) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	worker.submitMu.Lock()
	worker.mu.Lock()
	if !worker.stop {
		worker.stop = true
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

func (worker *Worker) run(ctx context.Context) {
	defer worker.wg.Done()
	defer worker.signalStopped()
	for {
		select {
		case <-ctx.Done():
			worker.drain()
			return
		case item := <-worker.queue:
			if item.fn == nil {
				item.result <- ErrWorkerFull
				continue
			}
			itemContext, cancel := context.WithCancel(ctx)
			stopRequest := context.AfterFunc(item.ctx, cancel)
			err := item.fn(itemContext)
			stopRequest()
			cancel()
			item.result <- err
		}
	}
}

func (worker *Worker) signalStopped() {
	worker.stopOnce.Do(func() { close(worker.done) })
}

func (worker *Worker) drain() {
	for {
		select {
		case item := <-worker.queue:
			item.result <- ErrWorkerStopped
		default:
			return
		}
	}
}
