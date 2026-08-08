package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestInitWorkspaceIsIdempotent(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	if err := initWorkspace(workspace); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	if err := initWorkspace(workspace); err != nil {
		t.Fatalf("re-init workspace: %v", err)
	}
	if err := validateWorkspace(workspace); err != nil {
		t.Fatalf("validate initialized workspace: %v", err)
	}
	for _, relative := range []string{schemaFile, indexFile, logFile} {
		if _, err := os.Stat(filepath.Join(workspace, relative)); err != nil {
			t.Errorf("expected initialized file %q: %v", relative, err)
		}
	}
}

func TestStoreDriverSelection(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "default", want: defaultStore},
		{name: postgresStore, value: postgresStore, want: postgresStore},
		{name: "unsupported", value: "mysql", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			viper.Reset()
			viper.Set(storeDriverKey, test.value)
			got, err := storeDriver()
			if test.wantErr {
				if err == nil {
					t.Fatal("storeDriver() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("storeDriver() error: %v", err)
			}
			if got != test.want {
				t.Fatalf("storeDriver() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRootExposesCurrentLifecycleCommands(t *testing.T) {
	root := newRootCommand()
	want := map[string]bool{initCommandName: true, "validate": true, "start": true, "ingest": true, "lint": true}
	for _, command := range root.Commands() {
		delete(want, command.Name())
	}
	if len(want) != 0 {
		t.Fatalf("missing lifecycle commands: %v", want)
	}
}
