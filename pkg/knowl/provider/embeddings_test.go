package provider

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	fixtureEmbeddingModel    = "selected-model"
	fixtureEmbeddingRevision = "revision"
	fixtureEmbeddingQuery    = "query: storage"
)

func TestEmbeddingClientRejectsServedModelMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"wrong-model","data":[{"index":0,"embedding":[1,0]}]}`))
	}))
	defer server.Close()
	client, err := NewEmbeddingClient(EmbeddingClientOptions{Endpoint: server.URL, Space: app.EmbeddingSpace{Model: fixtureEmbeddingModel, Revision: "fixed-revision", Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Embed(context.Background(), []string{"query: technical storage"})
	var failure *app.EmbeddingError
	if !errors.As(err, &failure) || failure.Code != knowl.RetrievalModelMismatch {
		t.Fatalf("served model mismatch accepted: %v", err)
	}
}

func TestEmbeddingClientValidatesBatchProtocol(t *testing.T) {
	for _, test := range []struct {
		name, body string
		count      int
		code       knowl.RetrievalFailure
	}{
		{"missing batch item", `{"model":"selected-model","data":[]}`, 1, knowl.RetrievalInvalidResponse},
		{"duplicate index", `{"model":"selected-model","data":[{"index":0,"embedding":[1,0]},{"index":0,"embedding":[1,0]}]}`, 2, knowl.RetrievalInvalidResponse},
		{"missing index", `{"model":"selected-model","data":[{"embedding":[1,0]}]}`, 1, knowl.RetrievalInvalidResponse},
		{"out-of-range index", `{"model":"selected-model","data":[{"index":1,"embedding":[1,0]}]}`, 1, knowl.RetrievalInvalidResponse},
		{"wrong dimension", `{"model":"selected-model","data":[{"index":0,"embedding":[1]}]}`, 1, knowl.RetrievalDimensionMismatch},
		{"null component", `{"model":"selected-model","data":[{"index":0,"embedding":[null,1]}]}`, 1, knowl.RetrievalInvalidResponse},
		{"zero vector", `{"model":"selected-model","data":[{"index":0,"embedding":[0,0]}]}`, 1, knowl.RetrievalInvalidResponse},
		{"overflow JSON number", `{"model":"selected-model","data":[{"index":0,"embedding":[1e999,0]}]}`, 1, knowl.RetrievalInvalidResponse},
		{"base64 vector", `{"model":"selected-model","data":[{"index":0,"embedding":"AAAA"}]}`, 1, knowl.RetrievalInvalidResponse},
		{"malformed JSON", `{`, 1, knowl.RetrievalInvalidResponse},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(test.body)) }))
			defer server.Close()
			client, err := NewEmbeddingClient(EmbeddingClientOptions{Endpoint: server.URL, Space: app.EmbeddingSpace{Model: fixtureEmbeddingModel, Revision: fixtureEmbeddingRevision, Dimensions: 2}})
			if err != nil {
				t.Fatal(err)
			}
			inputs := make([]string, test.count)
			for i := range inputs {
				inputs[i] = fixtureEmbeddingQuery
			}
			_, err = client.Embed(context.Background(), inputs)
			var failure *app.EmbeddingError
			if !errors.As(err, &failure) || failure.Code != test.code {
				t.Fatalf("response contract=%v want=%s", err, test.code)
			}
		})
	}
}

func TestEmbeddingClientSendsDeclaredInputsAndReordersNormalizedVectors(t *testing.T) {
	type request struct {
		Model    string   `json:"model"`
		Input    []string `json:"input"`
		Encoding string   `json:"encoding_format"`
	}
	captured := make(chan request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got request
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		captured <- got
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer operator-secret" {
			t.Error("wrong request boundary")
		}
		_, _ = w.Write([]byte(`{"model":"selected-model","usage":{"prompt_tokens":7},"data":[{"index":1,"embedding":[0,2]},{"index":0,"embedding":[3,4]}]}`))
	}))
	defer server.Close()
	client, err := NewEmbeddingClient(EmbeddingClientOptions{Endpoint: server.URL, APIKey: "operator-secret", Space: app.EmbeddingSpace{Model: fixtureEmbeddingModel, Revision: fixtureEmbeddingRevision, Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := client.Embed(context.Background(), []string{"query: Storage", "passage: Хранилище"})
	if err != nil {
		t.Fatal(err)
	}
	got := <-captured
	if got.Model != "selected-model" || got.Encoding != "float" || !reflect.DeepEqual(got.Input, []string{"query: Storage", "passage: Хранилище"}) {
		t.Fatalf("request=%#v", got)
	}
	if len(vectors) != 2 || math.Abs(float64(vectors[0][0])-0.6) > 1e-6 || math.Abs(float64(vectors[0][1])-0.8) > 1e-6 || vectors[1][0] != 0 || vectors[1][1] != 1 {
		t.Fatalf("normalized indexed vectors=%v", vectors)
	}
}

func TestEmbeddingClientEnforcesRequestAndResponseBounds(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		data := make([]map[string]any, len(request.Input))
		for i := range data {
			data[i] = map[string]any{"index": i, "embedding": []float64{1, 0}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "selected-model", "data": data})
	}))
	defer server.Close()
	client, err := NewEmbeddingClient(EmbeddingClientOptions{Endpoint: server.URL, Space: app.EmbeddingSpace{Model: fixtureEmbeddingModel, Revision: fixtureEmbeddingRevision, Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	atBound := make([]string, 16)
	for i := range atBound {
		atBound[i] = strings.Repeat("a", 2048)
	}
	if _, err := client.Embed(context.Background(), atBound); err != nil {
		t.Fatalf("at-bound batch rejected: %v", err)
	}
	for _, inputs := range [][]string{nil, append(append([]string(nil), atBound...), "extra"), {strings.Repeat("a", 2049)}, {"\xff"}, {"  "}, repeatEmbeddingInput(strings.Repeat("\x00", 2048), 16)} {
		if _, err := client.Embed(context.Background(), inputs); !errors.Is(err, app.ErrEmbedding) {
			t.Fatalf("invalid/oversized request accepted: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("rejected requests reached service: calls=%d", calls.Load())
	}
	large := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1))) }))
	defer large.Close()
	client, err = NewEmbeddingClient(EmbeddingClientOptions{Endpoint: large.URL, Space: app.EmbeddingSpace{Model: fixtureEmbeddingModel, Revision: fixtureEmbeddingRevision, Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Embed(context.Background(), []string{fixtureEmbeddingQuery})
	var failure *app.EmbeddingError
	if !errors.As(err, &failure) || failure.Code != knowl.RetrievalResponseLimit {
		t.Fatalf("oversized response=%v", err)
	}
}
func repeatEmbeddingInput(input string, count int) []string {
	result := make([]string, count)
	for i := range result {
		result[i] = input
	}
	return result
}

func TestEmbeddingClientDoesNotRedirectCredentials(t *testing.T) {
	var forwarded atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { forwarded.Add(1); w.WriteHeader(http.StatusOK) }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client, err := NewEmbeddingClient(EmbeddingClientOptions{Endpoint: source.URL, APIKey: "secret", Space: app.EmbeddingSpace{Model: fixtureEmbeddingModel, Revision: fixtureEmbeddingRevision, Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Embed(context.Background(), []string{fixtureEmbeddingQuery}); !errors.Is(err, app.ErrEmbedding) {
		t.Fatalf("redirect succeeded: %v", err)
	}
	if forwarded.Load() != 0 {
		t.Fatal("redirect destination received credentials/request")
	}
}

func TestEmbeddingClientCallerCancellationIsNotFallback(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { close(started); <-release }))
	defer server.Close()
	client, err := NewEmbeddingClient(EmbeddingClientOptions{Endpoint: server.URL, Space: app.EmbeddingSpace{Model: fixtureEmbeddingModel, Revision: fixtureEmbeddingRevision, Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.Embed(ctx, []string{fixtureEmbeddingQuery}); done <- err }()
	<-started
	cancel()
	err = <-done
	close(release)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation=%v", err)
	}
}

func TestEmbeddingClientProviderTimeoutHasSafeReason(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { <-release }))
	defer server.Close()
	defer close(release)
	client, err := NewEmbeddingClient(EmbeddingClientOptions{Endpoint: server.URL, Space: app.EmbeddingSpace{Model: fixtureEmbeddingModel, Revision: fixtureEmbeddingRevision, Dimensions: 2}})
	if err != nil {
		t.Fatal(err)
	}
	client.http.Timeout = 10 * time.Millisecond
	_, err = client.Embed(context.Background(), []string{fixtureEmbeddingQuery})
	var failure *app.EmbeddingError
	if !errors.As(err, &failure) || failure.Code != knowl.RetrievalDeadline {
		t.Fatalf("provider-local timeout=%v", err)
	}
}

func TestEmbeddingClientClassifiesStructuredValidationLimits(t *testing.T) {
	for _, test := range []struct {
		name, body string
		want       knowl.RetrievalFailure
	}{
		{"TEI validation", `{"message":"upstream text is not diagnostic authority","code":422,"type":"Validation"}`, knowl.RetrievalInputLimit},
		{"tokenizer failure", `{"code":422,"type":"Tokenizer"}`, knowl.RetrievalUnavailable},
		{"unknown type", `{"code":422,"type":"Backend"}`, knowl.RetrievalUnavailable},
		{"missing code", `{"type":"Validation"}`, knowl.RetrievalUnavailable},
		{"wrong code", `{"code":400,"type":"Validation"}`, knowl.RetrievalUnavailable},
		{"wrong shape", `{"code":"422","type":"Validation"}`, knowl.RetrievalUnavailable},
		{"plain prose", `Input validation error: too many tokens`, knowl.RetrievalUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client, err := NewEmbeddingClient(EmbeddingClientOptions{Endpoint: server.URL, Space: app.EmbeddingSpace{Model: fixtureEmbeddingModel, Revision: fixtureEmbeddingRevision, Dimensions: 2}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Embed(t.Context(), []string{fixtureEmbeddingQuery})
			var failure *app.EmbeddingError
			if !errors.As(err, &failure) || failure.Code != test.want {
				t.Fatalf("structured validation=%v want=%s", err, test.want)
			}
		})
	}
}
