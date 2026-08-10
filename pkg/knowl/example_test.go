package knowl_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
)

func ExampleNewHost() {
	root, err := os.MkdirTemp("", "knowl-host-example-")
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	host, err := knowl.NewHost(ctx, config, provider.Fixture{})
	if err != nil {
		fmt.Println(false)
		return
	}
	defer func() { _ = host.Close() }()

	fmt.Println(host != nil && !host.Ready())

	// Output:
	// true
}
