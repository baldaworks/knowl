package provider

import (
	"context"
	"sync"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/normahq/runtime/v2/agentfactory"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
)

// RuntimeFactory is the narrow shared-runtime seam used by RuntimeMaintainer.
// The concrete implementation is runtime/v2/agentfactory.Factory; keeping the
// consumer-side interface here makes deterministic tests independent of ACP or
// hosted provider processes.
type RuntimeFactory interface {
	Build(ctx context.Context, request agentfactory.BuildRequest) (adkagent.Agent, error)
}

// RuntimeFactoryValidator is implemented by the shared runtime factory when it
// can validate a provider schema without starting the provider.
type RuntimeFactoryValidator interface{ ValidateAgent(providerID string) error }

// RuntimeMaintainer adapts one Balda-compatible runtime provider to Knowl's
// structured app.Maintainer boundary.
type RuntimeMaintainer struct {
	factory    RuntimeFactory
	providerID string
	workspace  string
	lifetime   context.Context
	cancel     context.CancelFunc
	maxInput   int
	maxOutput  int
	newSession func() session.Service
	newRunner  runnerFactory

	mu      sync.Mutex
	runtime *maintainerRuntime
	closed  bool
}

// NewRuntimeMaintainer creates a lazy, structured maintainer for one runtime
// provider ID. The provider process is not built until Plan is called.
func NewRuntimeMaintainer(factory RuntimeFactory, providerID, workspace string) (*RuntimeMaintainer, error) {
	return newRuntimeMaintainer(factory, providerID, workspace, runtimeMaintainerOptions{})
}

var _ app.Maintainer = (*RuntimeMaintainer)(nil)
var _ app.HierarchyMaintainer = (*RuntimeMaintainer)(nil)
