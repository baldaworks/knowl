package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
)

const sidecarEmbeddingModel = "intfloat/multilingual-e5-small"

func TestEmbeddingSidecarConfigLoadsThroughProductionLoader(t *testing.T) {
	repo := testRepoRoot(t)
	fixture, err := os.ReadFile(filepath.Join(repo, "deploy", "sidecar", "embeddings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	working := t.TempDir()
	t.Chdir(working)
	clearKnowlEnv(t)
	configDir := filepath.Join(working, ".config", appName)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, err := loadConfig(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	config, err := hostConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	embedding := config.Embeddings
	if !embedding.Enabled || embedding.Endpoint != "http://tei:80/v1/embeddings" || embedding.Model != sidecarEmbeddingModel || embedding.Revision != "614241f622f53c4eeff9890bdc4f31cfecc418b3" || embedding.Dimensions != 384 || embedding.QueryPrefix != "query: " || embedding.PassagePrefix != "passage: " || embedding.FailurePolicy != app.EmbeddingFallbackLexical || embedding.APIKeyEnv != "" {
		t.Fatalf("embedding profile=%+v", embedding)
	}
	if len(config.Sources) != 2 || config.ListenAddr != net.JoinHostPort("0.0.0.0", "8080") {
		t.Fatalf("sidecar profile=%+v", config)
	}
}
