package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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
			ctx := context.WithValue(context.Background(), loadedConfigContextKey{}, loadedConfig{
				WorkingDir: t.TempDir(),
				Document: knowlConfigDocument{Knowl: AppConfig{Storage: StorageConfig{
					Type:   test.value,
					SQLite: &SQLiteConfig{},
				}}},
			})
			if test.value == postgresStore {
				loaded := ctx.Value(loadedConfigContextKey{}).(loadedConfig)
				loaded.Document.Knowl.Storage.SQLite = nil
				loaded.Document.Knowl.Storage.Postgres = &PostgresConfig{DSN: "postgres://localhost/knowl"}
				ctx = context.WithValue(context.Background(), loadedConfigContextKey{}, loaded)
			}
			got, err := storeDriver(ctx)
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
	want := map[string]bool{
		initCommandName:      true,
		validateCommandName:  true,
		migrateCommandName:   true,
		bootstrapCommandName: true,
		startCommandName:     true,
		ingestCommandName:    true,
		retrieveCommandName:  true,
		operationCommandName: true,
	}
	for _, command := range root.Commands() {
		delete(want, command.Name())
	}
	if len(want) != 0 {
		t.Fatalf("missing lifecycle commands: %v", want)
	}
}

func TestRootHelpExplainsSupportedLocalWorkflow(t *testing.T) {
	t.Parallel()

	output := commandHelpOutput(t, newRootCommand())
	for _, want := range []string{
		"Supported local workflow:",
		"knowl bootstrap wiki <path>",
		"knowl bootstrap obsidian <path>",
		"knowl bootstrap okf <path>",
		"knowl retrieve <text>",
		"knowl ingest --input request.json",
		"knowl operation <operation-id>",
		startCommandUsage,
		"Bootstrap creates a Knowl-owned workspace",
		"retained loopback HTTP/OpenAPI service mode",
		"same KISS contract for retrieve, ingest, operation, and health",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("root help missing %q in output:\n%s", want, output)
		}
	}
	for _, unwanted := range []string{
		"knowl search <text>",
		"knowl lint",
		"knowl page <page-id>",
		"knowl page links <page-id>",
		"review/apply",
	} {
		if strings.Contains(output, unwanted) {
			t.Fatalf("root help unexpectedly contains %q in output:\n%s", unwanted, output)
		}
	}
}

func TestImplementedWorkflowHelpDescribesCLIInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cmd       *cobra.Command
		args      []string
		wantParts []string
	}{
		{
			name: "bootstrap wiki",
			cmd:  newBootstrapCommand(),
			args: []string{bootstrapWikiName, commandHelpFlag},
			wantParts: []string{
				"<path>",
				"fresh Knowl workspace",
				"wiki/sources/bootstrap-wiki/**",
			},
		},
		{
			name: "retrieve",
			cmd:  newRetrieveCommand(),
			wantParts: []string{
				"positional arguments",
				workflowJSONStdoutHelp,
			},
		},
		{
			name: "ingest",
			cmd:  newIngestCommand(),
			wantParts: []string{
				"canonical JSON request body",
				workflowJSONStdoutHelp,
			},
		},
		{
			name: "operation",
			cmd:  newOperationCommand(),
			wantParts: []string{
				"durable operation ID",
				workflowJSONStdoutHelp,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			test.cmd.SetOut(&output)
			test.cmd.SetErr(&output)
			if len(test.args) == 0 {
				test.cmd.SetArgs([]string{commandHelpFlag})
			} else {
				test.cmd.SetArgs(test.args)
			}
			if err := test.cmd.Execute(); err != nil {
				t.Fatalf("%s help Execute() error: %v", test.name, err)
			}
			for _, want := range test.wantParts {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("%s help missing %q in output:\n%s", test.name, want, output.String())
				}
			}
		})
	}
}

func TestWorkflowCommandTreeCoversCurrentLocalSurface(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cmd       *cobra.Command
		wantShort string
		wantSubs  []string
	}{
		{
			name:      bootstrapCommandName,
			cmd:       newBootstrapCommand(),
			wantShort: "Bootstrap a Knowl workspace from an existing Markdown, Obsidian, or OKF tree",
			wantSubs:  []string{bootstrapWikiName, bootstrapObsidianName, bootstrapOKFName},
		},
		{
			name:      retrieveCommandName,
			cmd:       newRetrieveCommand(),
			wantShort: "Retrieve bounded evidence from Knowl",
			wantSubs:  nil,
		},
		{
			name:      ingestCommandName,
			cmd:       newIngestCommand(),
			wantShort: "Submit one source to the Knowl ingest pipeline",
			wantSubs:  nil,
		},
		{
			name:      operationCommandName,
			cmd:       newOperationCommand(),
			wantShort: "Read one durable operation status",
			wantSubs:  nil,
		},
		{
			name:      sourceCommandName,
			cmd:       newSourceCommand(),
			wantShort: "Inspect and synchronize configured knowledge sources",
			wantSubs:  []string{sourceListCommandName, sourceSyncCommandName, sourceStatusCommandName},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := test.cmd.Short; got != test.wantShort {
				t.Fatalf("%s short = %q, want %q", test.name, got, test.wantShort)
			}
			for _, want := range test.wantSubs {
				found := false
				for _, command := range test.cmd.Commands() {
					if command.Name() == want {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("%s is missing subcommand %q", test.name, want)
				}
			}
		})
	}
}
