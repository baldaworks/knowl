package knowlfx_test

import (
	"context"

	"github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	"github.com/baldaworks/knowl/pkg/knowlfx"
)

func ExampleNewApp() {
	config := knowl.DefaultConfig()
	config.Workspace = "/srv/knowledge"
	config.StorePath = "/srv/knowledge/.knowl/knowl.sqlite"

	application := knowlfx.NewApp(context.Background(), knowlfx.Options{
		Config:     config,
		Maintainer: provider.Fixture{},
	})
	_ = application
}
