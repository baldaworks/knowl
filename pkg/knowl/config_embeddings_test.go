package knowl

import (
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

func testEmbeddingConfig() EmbeddingsConfig {
	return EmbeddingsConfig{Enabled: true, Endpoint: "http://tei:80/v1/embeddings", Model: "intfloat/multilingual-e5-small", Revision: "614241f622f53c4eeff9890bdc4f31cfecc418b3", Dimensions: 384, QueryPrefix: "query: ", PassagePrefix: "passage: "}
}

func TestEmbeddingConfigRejectsInvalidEnabledShape(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*EmbeddingsConfig)
	}{
		{"missing endpoint", func(c *EmbeddingsConfig) { c.Endpoint = "" }},
		{"URL scheme", func(c *EmbeddingsConfig) { c.Endpoint = "file:///secret" }},
		{"URL userinfo", func(c *EmbeddingsConfig) { c.Endpoint = "https://secret@example.test/v1/embeddings" }},
		{"URL fragment", func(c *EmbeddingsConfig) { c.Endpoint += "#secret" }},
		{"missing model", func(c *EmbeddingsConfig) { c.Model = "" }},
		{"missing revision", func(c *EmbeddingsConfig) { c.Revision = "" }},
		{"zero dimension", func(c *EmbeddingsConfig) { c.Dimensions = 0 }},
		{"dimension overflow", func(c *EmbeddingsConfig) { c.Dimensions = 4097 }},
		{"model overflow", func(c *EmbeddingsConfig) { c.Model = strings.Repeat("m", 257) }},
		{"invalid identity", func(c *EmbeddingsConfig) { c.Revision = "revision\nsecret" }},
		{"invalid prefix", func(c *EmbeddingsConfig) { c.QueryPrefix = "\xff" }},
		{"prefix overflow", func(c *EmbeddingsConfig) { c.PassagePrefix = strings.Repeat("я", 129) }},
		{"unknown policy", func(c *EmbeddingsConfig) { c.FailurePolicy = "ignore" }},
		{"invalid env name", func(c *EmbeddingsConfig) { c.APIKeyEnv = "1KEY" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := testEmbeddingConfig()
			test.edit(&c)
			_, err := c.Normalize()
			var failure *app.EmbeddingError
			if !errors.As(err, &failure) || failure.Code != domain.RetrievalInvalidConfiguration {
				t.Fatalf("configuration accepted or wrong classified failure: %v", err)
			}
		})
	}
}

func TestEmbeddingConfigDisabledIgnoresAPIAndCredential(t *testing.T) {
	c := EmbeddingsConfig{Endpoint: "invalid", APIKeyEnv: "ABSENT_KNOWL_EMBEDDING_TEST_KEY", Dimensions: -1}
	got, err := c.Normalize()
	if err != nil || got != (EmbeddingsConfig{}) {
		t.Fatalf("disabled config=%#v err=%v", got, err)
	}
	secret, err := c.Credential()
	if err != nil || secret != "" {
		t.Fatal("disabled configuration resolved credentials")
	}
}

func TestEmbeddingConfigNormalizesBoundsWithoutChangingPrefixes(t *testing.T) {
	c := testEmbeddingConfig()
	c.Model = " " + strings.Repeat("m", 256) + " "
	c.Dimensions = 4096
	c.QueryPrefix = strings.Repeat("я", 128)
	got, err := c.Normalize()
	if err != nil || got.Model != strings.Repeat("m", 256) || got.QueryPrefix != c.QueryPrefix || got.FailurePolicy != app.EmbeddingFallbackLexical {
		t.Fatalf("normalized config=%#v err=%v", got, err)
	}
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.Embeddings = c
	if err := config.Validate(); err != nil {
		t.Fatalf("host rejected valid config: %v", err)
	}
	config.Embeddings.Dimensions = 4097
	if err := config.Validate(); !errors.Is(err, app.ErrEmbedding) {
		t.Fatalf("host accepted invalid config: %v", err)
	}
}

func TestEmbeddingCredentialMissingOrMalformedFailsRedacted(t *testing.T) {
	c := testEmbeddingConfig()
	c.APIKeyEnv = "KNOWL_TEST_EMBEDDING_SECRET"
	t.Setenv(c.APIKeyEnv, "")
	for _, value := range []string{"", "secret\r\nHeader: secret", strings.Repeat("s", 4097)} {
		t.Setenv(c.APIKeyEnv, value)
		if _, err := c.Credential(); !errors.Is(err, app.ErrEmbedding) {
			t.Fatalf("credential error=%v", err)
		}
	}
	t.Setenv(c.APIKeyEnv, "operator-secret")
	value, err := c.Credential()
	if err != nil || value != "operator-secret" {
		t.Fatalf("credential resolution failed: %v", err)
	}
}
