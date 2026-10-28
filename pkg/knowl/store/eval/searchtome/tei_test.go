package searchtome

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestLocalTEI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/info":
			_ = json.NewEncoder(w).Encode(map[string]any{teiModelID: ReferenceSpace.Model, teiModelSHA: ReferenceSpace.Revision, teiVersionField: teiVersion, teiModelDType: teiFloat32, teiAutoTruncate: false})
		case teiPath:
			v := make([]float32, 768)
			v[0] = 1
			_ = json.NewEncoder(w).Encode(map[string]any{"model": ReferenceSpace.Model, "data": []any{map[string]any{"index": 0, "embedding": v}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if _, _, err := LocalTEI(t.Context(), server.URL+teiPath); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"http://example.com/v1/embeddings", "http://user:secret@127.0.0.1/v1/embeddings", "http://127.0.0.1/v1/embeddings?secret=x", "https://127.0.0.1/v1/embeddings"} {
		if LocalEndpoint(u) {
			t.Fatalf("accepted endpoint %q", u)
		}
		if _, _, err := LocalTEI(t.Context(), u); err == nil {
			t.Fatalf("connected to %q", u)
		}
	}
}

func TestTEIMismatchStopsBeforeCorpusInference(t *testing.T) {
	for _, field := range []string{teiModelID, teiModelSHA, teiVersionField, teiModelDType, teiAutoTruncate} {
		t.Run(field, func(t *testing.T) {
			posts := 0
			info := map[string]any{teiModelID: ReferenceSpace.Model, teiModelSHA: ReferenceSpace.Revision, teiVersionField: teiVersion, teiModelDType: teiFloat32, teiAutoTruncate: false}
			if field == teiAutoTruncate {
				info[field] = true
			} else {
				info[field] = "wrong"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts++
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				_ = json.NewEncoder(w).Encode(info)
			}))
			defer server.Close()
			if _, _, err := LocalTEI(t.Context(), server.URL+teiPath); err == nil || posts != 0 {
				t.Fatalf("err=%v posts=%d", err, posts)
			}
		})
	}
}

func TestGemmaServerIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{teiModelID: Gemma2Space.Model, teiModelSHA: Gemma2Space.Revision, teiVersionField: "sentence-transformers/6.1.0", teiModelDType: teiFloat32, teiAutoTruncate: false})
	}))
	defer server.Close()
	if _, _, err := LocalEmbeddingServer(t.Context(), server.URL+teiPath, Gemma2Space); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LocalTEI(t.Context(), server.URL+teiPath); !errors.Is(err, ErrTEI) {
		t.Fatalf("wrong model accepted: %v", err)
	}
}

type batchCounter struct{ sizes []int }

func (b *batchCounter) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	b.sizes = append(b.sizes, len(inputs))
	return make([][]float32, len(inputs)), nil
}
func TestLocalBatchProvider(t *testing.T) {
	provider := &batchCounter{}
	got, err := (localBatchProvider{provider: provider}).Embed(t.Context(), make([]string, 10))
	if err != nil || len(got) != 10 || !reflect.DeepEqual(provider.sizes, []int{4, 4, 2}) {
		t.Fatalf("vectors=%d sizes=%v err=%v", len(got), provider.sizes, err)
	}
}

type sequenceProvider struct {
	calls int
	fail  bool
}

func (p *sequenceProvider) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	p.calls++
	if p.fail && p.calls == 2 {
		return nil, ErrTEI
	}
	result := make([][]float32, len(inputs))
	for i := range inputs {
		result[i] = []float32{float32((p.calls-1)*4 + i)}
	}
	return result, nil
}
func TestLocalBatchProviderOrderAndFailure(t *testing.T) {
	provider := &sequenceProvider{}
	vectors, err := (localBatchProvider{provider: provider}).Embed(t.Context(), make([]string, 10))
	if err != nil {
		t.Fatal(err)
	}
	for i, vector := range vectors {
		if vector[0] != float32(i) {
			t.Fatalf("vector %d = %v", i, vector)
		}
	}
	provider = &sequenceProvider{fail: true}
	vectors, err = (localBatchProvider{provider: provider}).Embed(t.Context(), make([]string, 10))
	if !errors.Is(err, ErrTEI) || vectors != nil {
		t.Fatalf("vectors=%v error=%v", vectors, err)
	}
}
