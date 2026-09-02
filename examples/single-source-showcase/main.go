package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "knowledge showcase: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting cwd: %w", err)
	}

	sourcesDir := filepath.Join(cwd, "examples", "single-source-showcase", "sources")
	if _, err := os.Stat(sourcesDir); err != nil {
		// Fallback for execution from inside examples/single-source-showcase
		sourcesDir = filepath.Join(cwd, "sources")
	}

	result, err := runKnowledgeShowcase(ctx, "", sourcesDir, nil)
	if err != nil {
		return fmt.Errorf("running showcase: %w", err)
	}

	printShowcaseSummary(result)
	return nil
}
