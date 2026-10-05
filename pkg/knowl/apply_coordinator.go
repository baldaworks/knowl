package knowl

import "context"

type hostApplyCoordinator struct{ gate chan struct{} }

func newHostApplyCoordinator() *hostApplyCoordinator {
	return &hostApplyCoordinator{gate: make(chan struct{}, 1)}
}
func (coordinator *hostApplyCoordinator) Acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case coordinator.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-coordinator.gate
		return nil, err
	}
	return func() { <-coordinator.gate }, nil
}
