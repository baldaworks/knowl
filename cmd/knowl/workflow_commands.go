package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/baldaworks/knowl/internal/httpapi/knowlapi"
	"github.com/spf13/cobra"
)

const (
	workflowInputFlagName  = "input"
	workflowInputFlagUsage = "--" + workflowInputFlagName
	loopbackListenAddr     = "127.0.0.1:0"
	queryFileCommandName   = "file"
	pageLinksCommandName   = "links"
	lintWorkflowPath       = "/v1/lint"
	workflowJSONStdoutHelp = "structured JSON result to stdout"
)

type workflowCommandError struct {
	StatusCode int
}

func (err *workflowCommandError) Error() string {
	return fmt.Sprintf("workflow request failed with status %d", err.StatusCode)
}

func newJSONBodyWorkflowCommand[T any](use, short, requestPath string) *cobra.Command {
	var inputPath string
	command := &cobra.Command{
		Use:           use,
		Short:         short,
		Long:          short + ".\n\nRead one canonical JSON request body from --input FILE|- and print the " + workflowJSONStdoutHelp + ".",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := readWorkflowInput(inputPath, cmd.InOrStdin())
			if err != nil {
				return err
			}
			normalized, err := normalizeWorkflowJSON[T](body)
			if err != nil {
				return fmt.Errorf("decode %s input: %w", cmd.CommandPath(), err)
			}
			return executeLocalWorkflowCommand(cmd, localWorkflowRequest{
				Method: http.MethodPost,
				Path:   requestPath,
				Body:   normalized,
			})
		},
	}
	command.Flags().StringVar(&inputPath, workflowInputFlagName, "", "JSON request body file path, or - for stdin")
	if err := command.MarkFlagRequired(workflowInputFlagName); err != nil {
		panic(err)
	}
	return command
}

func newApplyWorkflowCommand() *cobra.Command {
	return &cobra.Command{
		Use:           "apply <operation-id>",
		Short:         "Apply one staged operation",
		Long:          "Apply one staged operation.\n\nPass the durable operation ID as a positional argument. The command prints the " + workflowJSONStdoutHelp + ".",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			operationID := strings.TrimSpace(args[0])
			if operationID == "" {
				return fmt.Errorf("operation ID is required")
			}
			return executeLocalWorkflowCommand(cmd, localWorkflowRequest{
				Method: http.MethodPost,
				Path:   "/v1/operations/" + url.PathEscape(operationID) + "/apply",
			})
		},
	}
}

func newQueryFileCommand() *cobra.Command {
	return newJSONBodyWorkflowCommand[knowlapi.FilingRequest](
		queryFileCommandName,
		"File a typed query result through the normal ingest gate",
		"/v1/query/file",
	)
}

func newQueryReadCommand() *cobra.Command {
	return &cobra.Command{
		Use:           queryCommandName + " <text>",
		Short:         "Assemble bounded wiki references and citations",
		Long:          "Assemble bounded wiki references and citations.\n\nPass the query text as positional arguments. The command prints the " + workflowJSONStdoutHelp + ".",
		Args:          cobra.MinimumNArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeLocalWorkflowCommand(cmd, localWorkflowRequest{
				Method: http.MethodGet,
				Path:   "/v1/query?q=" + url.QueryEscape(strings.TrimSpace(strings.Join(args, " "))),
			})
		},
	}
}

func newSearchReadCommand() *cobra.Command {
	return &cobra.Command{
		Use:           searchCommandName + " <text>",
		Short:         "Search bounded page references",
		Long:          "Search bounded page references.\n\nPass the search text as positional arguments. The command prints the " + workflowJSONStdoutHelp + ".",
		Args:          cobra.MinimumNArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeLocalWorkflowCommand(cmd, localWorkflowRequest{
				Method: http.MethodGet,
				Path:   "/v1/search?q=" + url.QueryEscape(strings.TrimSpace(strings.Join(args, " "))),
			})
		},
	}
}

func newLintReadCommand() *cobra.Command {
	return &cobra.Command{
		Use:           lintCommandName,
		Short:         "Run deterministic workspace lint",
		Long:          "Run deterministic workspace lint.\n\nThe command prints the structured JSON lint report to stdout.",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return executeLocalWorkflowCommand(cmd, localWorkflowRequest{
				Method: http.MethodGet,
				Path:   lintWorkflowPath,
			})
		},
	}
}

func newOperationReadCommand() *cobra.Command {
	return &cobra.Command{
		Use:           operationCommandName + " <operation-id>",
		Short:         "Read one durable operation status",
		Long:          "Read one durable operation status.\n\nPass the durable operation ID as a positional argument. The command prints the " + workflowJSONStdoutHelp + ".",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			operationID := strings.TrimSpace(args[0])
			if operationID == "" {
				return fmt.Errorf("operation ID is required")
			}
			return executeLocalWorkflowCommand(cmd, localWorkflowRequest{
				Method: http.MethodGet,
				Path:   "/v1/operations/" + url.PathEscape(operationID),
			})
		},
	}
}

func newPageReadCommand() *cobra.Command {
	command := &cobra.Command{
		Use:           pageCommandName + " <page-id>",
		Short:         "Read one bounded canonical page",
		Long:          "Read one bounded canonical page.\n\nPass the canonical page ID as a positional argument. The command prints the " + workflowJSONStdoutHelp + ".",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			pageID := strings.TrimSpace(args[0])
			if pageID == "" {
				return fmt.Errorf("page ID is required")
			}
			return executeLocalWorkflowCommand(cmd, localWorkflowRequest{
				Method: http.MethodGet,
				Path:   "/v1/pages/" + url.PathEscape(pageID),
			})
		},
	}
	command.AddCommand(newPageLinksReadCommand())
	return command
}

func newPageLinksReadCommand() *cobra.Command {
	return &cobra.Command{
		Use:           pageLinksCommandName + " <page-id>",
		Short:         "Read one bounded link neighborhood",
		Long:          "Read one bounded link neighborhood.\n\nPass the canonical page ID as a positional argument. The command prints the " + workflowJSONStdoutHelp + ".",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			pageID := strings.TrimSpace(args[0])
			if pageID == "" {
				return fmt.Errorf("page ID is required")
			}
			return executeLocalWorkflowCommand(cmd, localWorkflowRequest{
				Method: http.MethodGet,
				Path:   "/v1/pages/" + url.PathEscape(pageID) + "/links",
			})
		},
	}
}

func executeLocalWorkflowCommand(cmd *cobra.Command, request localWorkflowRequest) error {
	response, err := newLocalWorkflowRunner().Execute(cmd.Context(), request)
	if err != nil {
		return err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if err := writeWorkflowBody(cmd.ErrOrStderr(), response.Body); err != nil {
			return err
		}
		return &workflowCommandError{StatusCode: response.StatusCode}
	}
	return writeWorkflowBody(cmd.OutOrStdout(), response.Body)
}

func readWorkflowInput(path string, stdin io.Reader) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("%s is required", workflowInputFlagName)
	}
	if path == "-" {
		content, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		return content, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read input file %q: %w", path, err)
	}
	return content, nil
}

func normalizeWorkflowJSON[T any](body []byte) ([]byte, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, fmt.Errorf("empty JSON input")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var value T
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("request contains multiple JSON values")
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode normalized JSON: %w", err)
	}
	return normalized, nil
}

func writeWorkflowBody(writer io.Writer, body []byte) error {
	if len(body) == 0 {
		return nil
	}
	if _, err := writer.Write(body); err != nil {
		return fmt.Errorf("write workflow response: %w", err)
	}
	if !bytes.HasSuffix(body, []byte{'\n'}) {
		if _, err := writer.Write([]byte{'\n'}); err != nil {
			return fmt.Errorf("terminate workflow response: %w", err)
		}
	}
	return nil
}
