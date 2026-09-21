// Command acpfixture is a deterministic ACP server used by real-binary tests.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	planEnv    = "KNOWL_TEST_ACP_PLAN"
	gateDirEnv = "KNOWL_TEST_ACP_GATE_DIR"
)

type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type promptParams struct {
	SessionID string `json:"sessionId"`
}

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var request envelope
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return fmt.Errorf("decode ACP request: %w", err)
		}
		switch request.Method {
		case "initialize":
			if err := respond(encoder, request.ID, map[string]any{"protocolVersion": 1}); err != nil {
				return err
			}
		case "session/new":
			if err := respond(encoder, request.ID, map[string]any{"sessionId": "knowl-fixture"}); err != nil {
				return err
			}
		case "session/prompt":
			var params promptParams
			if err := json.Unmarshal(request.Params, &params); err != nil {
				return fmt.Errorf("decode ACP prompt: %w", err)
			}
			if err := passGate(); err != nil {
				return err
			}
			var plan any
			if err := json.Unmarshal([]byte(os.Getenv(planEnv)), &plan); err != nil {
				return fmt.Errorf("decode fixture plan: %w", err)
			}
			planJSON, err := json.Marshal(plan)
			if err != nil {
				return fmt.Errorf("encode fixture plan: %w", err)
			}
			if err := encoder.Encode(envelope{
				JSONRPC: "2.0",
				Method:  "session/update",
				Params: mustJSON(map[string]any{
					"sessionId": params.SessionID,
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": string(planJSON)},
					},
				}),
			}); err != nil {
				return fmt.Errorf("write ACP plan update: %w", err)
			}
			if err := respond(encoder, request.ID, map[string]any{"stopReason": "end_turn"}); err != nil {
				return err
			}
		default:
			if len(request.ID) == 0 {
				continue
			}
			if err := encoder.Encode(envelope{
				JSONRPC: "2.0",
				ID:      request.ID,
				Error:   &rpcError{Code: -32601, Message: "method not supported by fixture"},
			}); err != nil {
				return fmt.Errorf("write ACP error: %w", err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read ACP request: %w", err)
	}
	return nil
}

func respond(encoder *json.Encoder, id json.RawMessage, result any) error {
	if err := encoder.Encode(envelope{JSONRPC: "2.0", ID: id, Result: result}); err != nil {
		return fmt.Errorf("write ACP response: %w", err)
	}
	return nil
}

func passGate() error {
	directory := os.Getenv(gateDirEnv)
	if directory == "" {
		return nil
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create fixture gate: %w", err)
	}
	invocation, err := os.CreateTemp(directory, "invocation-")
	if err != nil {
		return fmt.Errorf("record fixture invocation: %w", err)
	}
	if err := invocation.Close(); err != nil {
		return fmt.Errorf("close fixture invocation: %w", err)
	}
	entered := filepath.Join(directory, "entered")
	marker, err := os.OpenFile(entered, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		err = marker.Close()
	}
	if err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("record fixture entry: %w", err)
	}

	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(directory, "release")); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect fixture release: %w", err)
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("fixture gate timed out")
		case <-ticker.C:
		}
	}
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
