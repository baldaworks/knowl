package main

import (
	"errors"
	"os"

	"github.com/rs/zerolog/log"
)

func main() {
	initCommandLogging(os.Stderr)
	if err := newRootCommand().Execute(); err != nil {
		var workflowErr *workflowCommandError
		if errors.As(err, &workflowErr) {
			os.Exit(1)
		}
		log.Error().Err(err).Msg("knowl command failed")
		os.Exit(1)
	}
}
