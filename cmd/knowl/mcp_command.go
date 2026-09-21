package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/baldaworks/knowl/internal/mcpsdk"
	knowlruntime "github.com/baldaworks/knowl/pkg/knowl"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

const (
	mcpCommandName      = "mcp"
	mcpStdioCommandName = "stdio"
)

func newMCPCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   mcpCommandName,
		Short: "Run a Knowl Model Context Protocol transport",
	}
	command.AddCommand(&cobra.Command{
		Use:   mcpStdioCommandName,
		Short: "Serve the project-scoped Knowl tools over stdio",
		Long: "Serve the project-scoped Knowl tools over MCP stdio. " +
			"The client owns the process lifetime; no HTTP listener, operator token, or source scheduler is used. " +
			"Standard output is reserved exclusively for MCP JSON-RPC traffic.",
		Args: cobra.NoArgs,
		RunE: runMCPStdio,
	})
	return command
}

func runMCPStdio(cmd *cobra.Command, _ []string) error {
	runtimeFactory, providerID, err := selectedRuntimeProvider(cmd.Context())
	if err != nil {
		return err
	}
	config, err := hostConfig(cmd.Context())
	if err != nil {
		return err
	}
	// These HTTP-only values are deliberately irrelevant to stdio operation.
	config.ListenAddr = "127.0.0.1:0"
	config.OperatorToken = ""
	ctx, stopSignal := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stopSignal()

	host, err := knowlruntime.New(ctx, selectedHostOptions(config, runtimeFactory, providerID))
	if err != nil {
		return err
	}
	stopHost := func() error {
		stopCtx, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
		defer cancel()
		return host.Stop(stopCtx)
	}
	if err := host.StartOperationWorker(ctx); err != nil {
		return errors.Join(err, stopHost())
	}
	server, err := mcpsdk.NewServer(host.MCP())
	if err != nil {
		return errors.Join(err, stopHost())
	}
	runErr := server.Run(ctx, &sdkmcp.StdioTransport{})
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		runErr = nil
	}
	if runErr != nil {
		runErr = fmt.Errorf("run Knowl MCP stdio: %w", runErr)
	}
	return errors.Join(runErr, stopHost())
}
