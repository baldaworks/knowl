package main

import (
	"errors"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
)

// Catch accepting an enabled embedding service without its model/API identity.
func TestLoadConfigRejectsIncompleteEnabledEmbeddings(t *testing.T) {
	_, err := tryLoadTestConfig(t, testConfigOptions{knowl: "provider: codex\nembeddings:\n  enabled: true\n"})
	if !errors.Is(err, app.ErrEmbedding) {
		t.Fatalf("expected classified invalid embedding configuration: %v", err)
	}
}

func TestHostConfigLoadsTypedEmbeddingService(t *testing.T) {
	ctx := loadTestConfig(t, testConfigOptions{knowl: `provider: codex
embeddings:
  enabled: true
  endpoint: http://tei:80/v1/embeddings
  model: intfloat/multilingual-e5-small
  revision: 614241f622f53c4eeff9890bdc4f31cfecc418b3
  dimensions: 384
  query_prefix: 'query: '
  passage_prefix: 'passage: '
  api_key_env: KNOWL_TEST_API_KEY
  failure_policy: strict
`})
	c, err := hostConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e := c.Embeddings
	if !e.Enabled || e.Endpoint != "http://tei:80/v1/embeddings" || e.Model != "intfloat/multilingual-e5-small" || e.Dimensions != 384 || e.QueryPrefix != "query: " || e.PassagePrefix != "passage: " || e.APIKeyEnv != "KNOWL_TEST_API_KEY" || e.FailurePolicy != "strict" {
		t.Fatalf("lost typed service configuration: %#v", e)
	}
}
