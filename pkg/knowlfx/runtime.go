package knowlfx

import (
	"context"

	"github.com/baldaworks/knowl/pkg/knowl"
	"go.uber.org/fx"
)

type Options = knowl.Options
type Host = knowl.Host

// Module exposes the public Fx module for one Knowl host. Host assembly
// delegates to the plain-Go constructor in root pkg/knowl.
func Module(ctx context.Context, options Options) fx.Option {
	if ctx == nil {
		ctx = context.Background()
	}
	return fx.Module("knowlfx",
		fx.Provide(
			func() (*Host, error) { return knowl.New(ctx, options) },
			func(host *Host) hostLifecycle { return host },
		),
		fx.Invoke(registerHostLifecycle),
	)
}

// NewApp builds an fx.App for one Knowl host.
func NewApp(ctx context.Context, options Options, additional ...fx.Option) *fx.App {
	return fx.New(
		Module(ctx, options),
		fx.Options(additional...),
	)
}

type hostLifecycle interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

type hostLifecycleParams struct {
	fx.In

	Lifecycle fx.Lifecycle
	Host      hostLifecycle
}

func registerHostLifecycle(params hostLifecycleParams) {
	params.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error { return nil },
		OnStop:  params.Host.Stop,
	})
	params.Lifecycle.Append(fx.Hook{
		OnStart: params.Host.Start,
	})
}
