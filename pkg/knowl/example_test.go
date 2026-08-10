package knowl_test

import (
	"context"

	"github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
)

func ExampleNewHost() {
	config := knowl.DefaultConfig()
	config.Workspace = "/srv/knowledge"
	config.StorePath = "/srv/knowledge/.knowl/knowl.sqlite"

	_, _ = knowl.NewHost(context.Background(), config, provider.Fixture{})
}
