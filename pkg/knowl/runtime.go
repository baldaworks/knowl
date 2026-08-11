package knowl

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/mcp"
	runtimeprovider "github.com/baldaworks/knowl/pkg/knowl/provider"
)

// Options supplies the host configuration plus either an explicit maintainer
// or the runtime-provider inputs needed to build one lazily.
type Options struct {
	Config         Config
	Maintainer     app.Maintainer
	RuntimeFactory runtimeprovider.RuntimeFactory
	ProviderID     string
}

// Host composes canonical content, operational state, application services, HTTP, and MCP.
type Host struct {
	config           Config
	workspace        *contentfs.Workspace
	closer           io.Closer
	maintainerCloser io.Closer

	operations app.OperationStore
	index      app.SearchIndex
	worker     *worker
	service    *app.IngestService
	query      *app.QueryService
	lint       *app.LintService
	mcp        *mcp.Server
	handler    http.Handler

	ready     atomic.Bool
	mu        sync.Mutex
	server    *http.Server
	listener  net.Listener
	cancel    context.CancelFunc
	started   bool
	closed    bool
	serverErr chan error
}

// NewHost is an explicit constructor alias for callers composing one local host.
func NewHost(ctx context.Context, config Config, maintainer app.Maintainer) (*Host, error) {
	return New(ctx, Options{Config: config, Maintainer: maintainer})
}
