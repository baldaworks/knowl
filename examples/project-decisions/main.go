package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "project-decisions host: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultRunTimeout)
	defer cancel()
	result, err := runProjectDecisions(ctx, clientConfig{
		Endpoint: os.Getenv("KNOWL_MCP_ENDPOINT"), OperatorToken: os.Getenv("KNOWL_OPERATOR_TOKEN"),
	})
	if err != nil {
		return err
	}
	for _, operation := range result.Operations {
		fmt.Printf("durable source %s@%s -> operation %s (%s)\n", operation.Source, operation.Revision, operation.OperationID, operation.Status)
	}
	for _, evidence := range result.Evidence {
		fmt.Printf("untrusted evidence %s refs=%v: %s\n", evidence.PageID, evidence.SourceRefs, evidence.Snippet)
	}
	answer, err := hostAnswer(result)
	if err != nil {
		return err
	}
	fmt.Println(answer)
	return nil
}
