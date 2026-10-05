package knowl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/provider"
)

func TestWorkerPreflightRejectsBeforeWorkspaceStartup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		workers int
	}{
		{"negative", -1}, {"above_bound", 3}, {"singleton_two", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.Workers = tc.workers
			config.Workspace = filepath.Join(t.TempDir(), "unopened")
			host, err := New(context.Background(), Options{Config: config, Maintainer: provider.Fixture{}})
			if host != nil {
				_ = host.Close()
				t.Fatal("unsupported host started")
			}
			if !errors.Is(err, ErrWorkerConfigInvalid) {
				t.Fatalf("preflight = %v", err)
			}
			if _, err := os.Stat(config.Workspace); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("workspace was opened: %v", err)
			}
		})
	}
}

func TestWorkerCapacityDefaultsAndBounds(t *testing.T) {
	for _, workers := range []int{0, 1, 2} {
		config := DefaultConfig()
		config.Workspace = t.TempDir()
		config.Workers = workers
		got, err := config.normalized()
		if err != nil {
			t.Fatal(err)
		}
		want := workers
		if want == 0 {
			want = 1
		}
		if got.Workers != want {
			t.Fatalf("workers = %d, want %d", got.Workers, want)
		}
	}
}
