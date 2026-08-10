package knowlfx_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
	"github.com/baldaworks/knowl/pkg/knowlfx"
	"go.uber.org/fx"
)

func ExampleNewApp() {
	root, err := os.MkdirTemp("", "knowl-fx-example-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(root) }()

	workspace, err := contentfs.New(root)
	if err != nil {
		panic(err)
	}
	if err := workspace.Init(); err != nil {
		panic(err)
	}

	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "knowl.sqlite")
	config.ListenAddr = "127.0.0.1:0"

	var host *knowl.Host
	application := knowlfx.NewApp(context.Background(), knowlfx.Options{
		Config:     config,
		Maintainer: provider.Fixture{},
	}, fx.Populate(&host))
	if err := application.Err(); err != nil {
		fmt.Println(false)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := application.Start(ctx); err != nil {
		fmt.Println(false)
		return
	}
	ready := host != nil && host.Ready()
	if err := application.Stop(ctx); err != nil {
		fmt.Println(false)
		return
	}

	fmt.Println(ready)

	// Output:
	// true
}
