package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/spf13/cobra"
)

func TestExportCommandsComposeWithoutProvider(t *testing.T) {
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), loadedConfigContextKey{}, loadedConfig{
		Document: knowlConfigDocument{Knowl: AppConfig{
			Provider: "unavailable-acp", Workspace: WorkspaceConfig{Path: workspace.Root()}, Scope: knowl.ScopeRef("local"),
		}},
		WorkingDir: workspace.Root(),
	})

	destination := filepath.Join(t.TempDir(), "public")
	command := newExportCommand()
	if _, stderr, err := executeExportCommand(command, ctx, []string{exportOKFName, "--output", destination}); err != nil {
		t.Fatalf("export okf: %v, stderr=%s", err, stderr)
	}
	llmsPath := filepath.Join(destination, "llms.txt")
	command = newExportCommand()
	if stdout, stderr, err := executeExportCommand(command, ctx, []string{exportLLMsTxtName, "--output", llmsPath}); err != nil || stdout != "" {
		t.Fatalf("export llms.txt: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	data, err := os.ReadFile(llmsPath)
	if err != nil || string(data) != "# Knowl Index\n" {
		t.Fatalf("llms.txt = %q, %v", data, err)
	}
}

func TestExportLLMsTxtWritesRawStdout(t *testing.T) {
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), loadedConfigContextKey{}, loadedConfig{
		Document:   knowlConfigDocument{Knowl: AppConfig{Workspace: WorkspaceConfig{Path: workspace.Root()}}},
		WorkingDir: workspace.Root(),
	})
	command := newExportCommand()
	stdout, stderr, err := executeExportCommand(command, ctx, []string{exportLLMsTxtName, "--title", "Public", "--summary", "Read selectively"})
	if err != nil || stderr != "" || stdout != "# Public\n\n> Read selectively\n" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
}

func TestExportCommandSurface(t *testing.T) {
	command := newExportCommand()
	if len(command.Commands()) != 2 || command.Commands()[0].Name() != exportLLMsTxtName && command.Commands()[1].Name() != exportLLMsTxtName {
		t.Fatalf("subcommands = %#v", command.Commands())
	}
	if !strings.Contains(commandHelpOutput(t, command), "llms-txt") {
		t.Fatal("export help omits llms-txt")
	}
}

func TestWriteExportFilePreservesExistingOnFailure(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "llms.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeExportFile(filepath.Join(path, "child"), []byte("new")); err == nil {
		t.Fatal("writeExportFile() error = nil")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "old" {
		t.Fatalf("existing output = %q, %v", data, err)
	}
	matches, err := filepath.Glob(filepath.Join(directory, ".knowl-llms-txt-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files = %v, %v", matches, err)
	}
}

func executeExportCommand(command *cobra.Command, ctx context.Context, args []string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	command.SetContext(ctx)
	command.SetArgs(args)
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}
