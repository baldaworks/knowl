package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	embeddingBatchLimit     = 16
	embeddingInputBytes     = 2048
	embeddingRequestBytes   = 64 << 10
	embeddingResponseBytes  = 1 << 20
	embeddingRequestTimeout = 10 * time.Second
)

// EmbeddingClientOptions configures one immutable API contract. APIKey is
// supplied explicitly at construction and is never serialized or reported.
type EmbeddingClientOptions struct {
	Endpoint string
	Space    app.EmbeddingSpace
	APIKey   string
}

// EmbeddingClient speaks bounded OpenAI-compatible float embedding requests.
type EmbeddingClient struct {
	endpoint string
	space    app.EmbeddingSpace
	key      string
	http     *http.Client
	slots    chan struct{}
}

// NewEmbeddingClient validates one operator-owned API/model configuration.
func NewEmbeddingClient(options EmbeddingClientOptions) (*EmbeddingClient, error) {
	space, err := app.NormalizeEmbeddingSpace(options.Space)
	u, urlErr := url.Parse(options.Endpoint)
	if err != nil || urlErr != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(options.Endpoint) > 2048 || len(options.APIKey) > 4096 || strings.ContainsAny(options.APIKey, "\r\n") {
		return nil, embeddingFailure(knowl.RetrievalInvalidConfiguration)
	}
	return &EmbeddingClient{endpoint: options.Endpoint, space: space, key: options.APIKey, http: &http.Client{Timeout: embeddingRequestTimeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, slots: make(chan struct{}, 4)}, nil
}

// Embed returns complete indexed normalized vectors or a safe classified error.
// Caller cancellation takes precedence over provider-local degradation reasons.
func (client *EmbeddingClient) Embed(ctx context.Context, inputs []string) (output [][]float32, resultErr error) {
	if ctx == nil {
		return nil, embeddingFailure(knowl.RetrievalInvalidInput)
	}
	defer func() {
		if err := ctx.Err(); err != nil {
			output, resultErr = nil, err
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(inputs) == 0 || len(inputs) > embeddingBatchLimit {
		return nil, embeddingFailure(knowl.RetrievalInputLimit)
	}
	for _, input := range inputs {
		if !utf8.ValidString(input) || strings.TrimSpace(input) == "" {
			return nil, embeddingFailure(knowl.RetrievalInvalidInput)
		}
		if len(input) > embeddingInputBytes {
			return nil, embeddingFailure(knowl.RetrievalInputLimit)
		}
	}
	payload, err := json.Marshal(struct {
		Model    string   `json:"model"`
		Input    []string `json:"input"`
		Encoding string   `json:"encoding_format"`
	}{Model: client.space.Model, Input: inputs, Encoding: "float"})
	if err != nil || len(payload) > embeddingRequestBytes {
		return nil, embeddingFailure(knowl.RetrievalInputLimit)
	}
	callCtx, cancel := context.WithTimeout(ctx, embeddingRequestTimeout)
	defer cancel()
	select {
	case client.slots <- struct{}{}:
		defer func() { <-client.slots }()
	case <-callCtx.Done():
		return nil, embeddingCallFailure(ctx, callCtx, callCtx.Err())
	}
	request, err := http.NewRequestWithContext(callCtx, http.MethodPost, client.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, embeddingFailure(knowl.RetrievalInvalidConfiguration)
	}
	request.Header.Set("Content-Type", "application/json")
	if client.key != "" {
		request.Header.Set("Authorization", "Bearer "+client.key)
	}
	response, err := client.http.Do(request)
	if err != nil {
		return nil, embeddingCallFailure(ctx, callCtx, err)
	}
	defer func() { _ = response.Body.Close() }()
	encoded, err := io.ReadAll(io.LimitReader(response.Body, embeddingResponseBytes+1))
	if err != nil {
		return nil, embeddingCallFailure(ctx, callCtx, err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(encoded) > embeddingResponseBytes {
		return nil, embeddingFailure(knowl.RetrievalResponseLimit)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusRequestEntityTooLarge {
			return nil, embeddingFailure(knowl.RetrievalInputLimit)
		}
		return nil, embeddingFailure(knowl.RetrievalUnavailable)
	}
	var result struct {
		Model string `json:"model"`
		Data  []struct {
			Index     *int       `json:"index"`
			Embedding []*float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, embeddingFailure(knowl.RetrievalInvalidResponse)
	}
	if result.Model != client.space.Model {
		return nil, embeddingFailure(knowl.RetrievalModelMismatch)
	}
	if len(result.Data) != len(inputs) {
		return nil, embeddingFailure(knowl.RetrievalInvalidResponse)
	}
	vectors := make([][]float32, len(inputs))
	for _, entry := range result.Data {
		if entry.Index == nil || *entry.Index < 0 || *entry.Index >= len(inputs) || vectors[*entry.Index] != nil {
			return nil, embeddingFailure(knowl.RetrievalInvalidResponse)
		}
		if len(entry.Embedding) != client.space.Dimensions {
			return nil, embeddingFailure(knowl.RetrievalDimensionMismatch)
		}
		values := make([]float64, len(entry.Embedding))
		for i, value := range entry.Embedding {
			if value == nil {
				return nil, embeddingFailure(knowl.RetrievalInvalidResponse)
			}
			values[i] = *value
		}
		vector, err := normalizeEmbeddingVector(values)
		if err != nil {
			return nil, err
		}
		vectors[*entry.Index] = vector
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return vectors, nil
}

func normalizeEmbeddingVector(input []float64) ([]float32, error) {
	// Scaled sum-of-squares avoids norm overflow for finite large components.
	scale := 0.0
	for _, value := range input {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, embeddingFailure(knowl.RetrievalInvalidResponse)
		}
		scale = math.Max(scale, math.Abs(value))
	}
	if scale == 0 {
		return nil, embeddingFailure(knowl.RetrievalInvalidResponse)
	}
	sum := 0.0
	for _, value := range input {
		v := value / scale
		sum += v * v
	}
	norm := math.Sqrt(sum)
	output := make([]float32, len(input))
	check := 0.0
	for i, value := range input {
		v := float32((value / scale) / norm)
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, embeddingFailure(knowl.RetrievalInvalidResponse)
		}
		output[i] = v
		check += float64(v) * float64(v)
	}
	if check == 0 {
		return nil, embeddingFailure(knowl.RetrievalInvalidResponse)
	}
	return output, nil
}

func embeddingFailure(code knowl.RetrievalFailure) error { return &app.EmbeddingError{Code: code} }
func embeddingCallFailure(parent, call context.Context, cause error) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if call.Err() != nil || errors.Is(cause, context.DeadlineExceeded) {
		return embeddingFailure(knowl.RetrievalDeadline)
	}
	return embeddingFailure(knowl.RetrievalUnavailable)
}
