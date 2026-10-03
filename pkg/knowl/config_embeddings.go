package knowl

import (
	"net/url"
	"os"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

// EmbeddingsConfig controls an optional separate embedding API. The zero value
// is inert lexical-only; disabled fields and credential references are ignored.
type EmbeddingsConfig struct {
	Enabled       bool                       `mapstructure:"enabled"`
	Endpoint      string                     `mapstructure:"endpoint"`
	Model         string                     `mapstructure:"model"`
	Revision      string                     `mapstructure:"revision"`
	Dimensions    int                        `mapstructure:"dimensions"`
	QueryPrefix   string                     `mapstructure:"query_prefix"`
	PassagePrefix string                     `mapstructure:"passage_prefix"`
	APIKeyEnv     string                     `mapstructure:"api_key_env"`
	FailurePolicy app.EmbeddingFailurePolicy `mapstructure:"failure_policy"`
}

// Normalize validates shape without opening files, sockets or reading secrets.
func (config EmbeddingsConfig) Normalize() (EmbeddingsConfig, error) {
	if !config.Enabled {
		return EmbeddingsConfig{}, nil
	}
	config.Endpoint = strings.TrimSpace(config.Endpoint)
	config.Model = strings.TrimSpace(config.Model)
	config.Revision = strings.TrimSpace(config.Revision)
	config.APIKeyEnv = strings.TrimSpace(config.APIKeyEnv)
	if config.FailurePolicy == "" {
		config.FailurePolicy = app.EmbeddingFallbackLexical
	}
	_, spaceErr := app.NormalizeEmbeddingSpace(config.Space())
	u, err := url.Parse(config.Endpoint)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(config.Endpoint) > 2048 ||
		spaceErr != nil ||
		(config.FailurePolicy != app.EmbeddingFallbackLexical && config.FailurePolicy != app.EmbeddingStrict) || !embeddingEnvName(config.APIKeyEnv) {
		return EmbeddingsConfig{}, &app.EmbeddingError{Code: domain.RetrievalInvalidConfiguration}
	}
	return config, nil
}

// Credential resolves only the explicitly configured bearer reference.
func (config EmbeddingsConfig) Credential() (string, error) {
	if !config.Enabled || config.APIKeyEnv == "" {
		return "", nil
	}
	value, ok := os.LookupEnv(config.APIKeyEnv)
	if !ok || value == "" || len(value) > 4096 || strings.ContainsAny(value, "\r\n") {
		return "", &app.EmbeddingError{Code: domain.RetrievalInvalidConfiguration}
	}
	return value, nil
}

// Space contains only nonsecret output-affecting model identity.
func (config EmbeddingsConfig) Space() app.EmbeddingSpace {
	return app.EmbeddingSpace{Model: config.Model, Revision: config.Revision, Dimensions: config.Dimensions, QueryPrefix: config.QueryPrefix, PassagePrefix: config.PassagePrefix}
}

func embeddingEnvName(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 256 {
		return false
	}
	for i, r := range value {
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}
