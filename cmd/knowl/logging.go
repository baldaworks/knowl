package main

import (
	"io"
	"log/slog"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func initCommandLogging(output io.Writer) {
	if output == nil {
		output = io.Discard
	}

	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	logger := newConsoleLogger(output)
	log.Logger = logger
	zerolog.DefaultContextLogger = &log.Logger

	slog.SetDefault(slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
}

func commandLogger(cmd *cobra.Command) *zerolog.Logger {
	if cmd == nil {
		return &log.Logger
	}
	logger := newConsoleLogger(cmd.ErrOrStderr())
	return &logger
}

func newConsoleLogger(output io.Writer) zerolog.Logger {
	if output == nil {
		output = io.Discard
	}
	return zerolog.New(zerolog.ConsoleWriter{
		Out:        zerolog.SyncWriter(output),
		TimeFormat: time.RFC3339,
		NoColor:    true,
	}).With().Timestamp().Logger()
}
