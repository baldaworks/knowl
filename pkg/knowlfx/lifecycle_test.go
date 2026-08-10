package knowlfx

import (
	"context"
	"sync/atomic"
	"testing"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

type testLifecycleHost struct {
	starts atomic.Int32
	stops  atomic.Int32
}

func (host *testLifecycleHost) Start(context.Context) error {
	host.starts.Add(1)
	return nil
}

func (host *testLifecycleHost) Stop(context.Context) error {
	host.stops.Add(1)
	return nil
}

func TestRegisterHostLifecycle(t *testing.T) {
	host := new(testLifecycleHost)
	application := fxtest.New(t,
		fx.Provide(func() hostLifecycle { return host }),
		fx.Invoke(registerHostLifecycle),
	)
	application.RequireStart()
	if got := host.starts.Load(); got != 1 {
		t.Fatalf("start calls = %d, want 1", got)
	}
	application.RequireStop()
	if got := host.stops.Load(); got != 1 {
		t.Fatalf("stop calls = %d, want 1", got)
	}
}
