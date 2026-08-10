package knowlfx_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowlfx"
	"github.com/normahq/runtime/v2/agentfactory"
	"go.uber.org/fx"
	adkagent "google.golang.org/adk/v2/agent"
)

func TestNewAppBuildsRuntimeMaintainerThroughFx(t *testing.T) {
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	config := knowl.DefaultConfig()
	config.Workspace = workspace.Root()
	config.StorePath = filepath.Join(workspace.Root(), ".knowl", "state.db")
	config.ListenAddr = "127.0.0.1:0"
	factory := &validatingRuntimeFactory{providerID: "provider"}
	var host *knowl.Host
	application := knowlfx.NewApp(context.Background(), knowlfx.Options{
		Config:         config,
		RuntimeFactory: factory,
		ProviderID:     "provider",
	}, fx.Populate(&host))
	if err := application.Err(); err != nil {
		t.Fatalf("Fx composition error: %v", err)
	}
	startCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := application.Start(startCtx); err != nil {
		t.Fatalf("Fx start error: %v", err)
	}
	if host == nil || !host.Ready() {
		t.Fatalf("host = %#v, want ready host", host)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := application.Stop(stopCtx); err != nil {
		t.Fatalf("Fx stop error: %v", err)
	}
	if factory.validations != 1 {
		t.Fatalf("provider validations = %d, want one", factory.validations)
	}
	if factory.builds != 0 {
		t.Fatalf("provider builds = %d, want lazy provider", factory.builds)
	}
}

type validatingRuntimeFactory struct {
	providerID  string
	validations int
	builds      int
}

func (factory *validatingRuntimeFactory) ValidateAgent(providerID string) error {
	factory.validations++
	if providerID != factory.providerID {
		return errors.New("unexpected provider ID")
	}
	return nil
}

func (factory *validatingRuntimeFactory) Build(context.Context, agentfactory.BuildRequest) (adkagent.Agent, error) {
	factory.builds++
	return nil, errors.New("provider build should remain lazy")
}
