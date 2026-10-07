package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
)

const sidecarEmbeddingModel = "intfloat/multilingual-e5-base"

func TestEmbeddingSidecarConfigLoadsThroughProductionLoader(t *testing.T) {
	repo := testRepoRoot(t)
	fixture, err := os.ReadFile(filepath.Join(repo, "deploy", "sidecar", "embeddings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	working := t.TempDir()
	t.Chdir(working)
	clearKnowlEnv(t)
	t.Setenv("OPENAI_API_KEY", "test-api-key")
	t.Setenv("OPENAI_MODEL", "test-model")
	t.Setenv(operatorTokenEnvName, "test-operator-token")
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
	hybrid, err := configFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	baseFixture, err := os.ReadFile(filepath.Join(repo, "deploy", "sidecar", "quickstart.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), baseFixture, 0o600); err != nil {
		t.Fatal(err)
	}
	baseCtx, err := loadConfig(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	base, err := configFromContext(baseCtx)
	if err != nil {
		t.Fatal(err)
	}
	hybrid.Document.Knowl.Embeddings = base.Document.Knowl.Embeddings
	if !reflect.DeepEqual(hybrid.Document, base.Document) {
		t.Fatal("enabling embeddings changed non-embedding configuration")
	}
	embedding := config.Embeddings
	if !embedding.Enabled || embedding.Endpoint != "http://tei:80/v1/embeddings" || embedding.Model != sidecarEmbeddingModel || embedding.Revision != "d128750597153bb5987e10b1c3493a34e5a4502a" || embedding.Dimensions != 768 || embedding.QueryPrefix != "query: " || embedding.PassagePrefix != "passage: " || embedding.FailurePolicy != app.EmbeddingFallbackLexical || embedding.APIKeyEnv != "" {
		t.Fatalf("embedding profile=%+v", embedding)
	}
	if len(config.Sources) != 1 || config.ListenAddr != net.JoinHostPort("0.0.0.0", "8080") {
		t.Fatalf("sidecar profile=%+v", config)
	}
}
