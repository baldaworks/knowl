// Package writelock provides the store write mutex with cancellable acquisition.
package writelock

import (
	"context"
	"sync"
)

// Mutex has a usable zero value and must not be copied after first use.
type Mutex struct {
	once  sync.Once
	token chan struct{}
}

func (m *Mutex) Lock() { _ = m.LockContext(context.Background()) }

func (m *Mutex) LockContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.once.Do(func() { m.token = make(chan struct{}, 1) })
	select {
	case m.token <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Mutex) Unlock() { <-m.token }
