package main

import (
	"errors"
	"fmt"
	"os"
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		var workflowErr *workflowCommandError
		if errors.As(err, &workflowErr) {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
