package knowl

import (
	"context"
	"testing"
)

func TestOpenStoreExposesSourceStateWithoutConfiguredSources(t *testing.T) {
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.StorePath = config.Workspace + "/state.sqlite"
	config.Sources = nil

	store, err := openStore(context.Background(), config)
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	t.Cleanup(func() { _ = store.closer.Close() })
	if store.operations == nil || store.index == nil || store.sources == nil {
		t.Fatalf("openStore() ports = operations:%v index:%v sources:%v", store.operations != nil, store.index != nil, store.sources != nil)
	}
}
