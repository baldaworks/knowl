package provider

import (
	"errors"
	"testing"
	"time"
)

func TestConfigValidatesIndependentProviderSettings(t *testing.T) {
	config := Config{ID: "fixture", Model: "cheap-model", Timeout: time.Second, MaxInputBytes: 1024, MaxOutputBytes: 2048, MaxOutputItems: 8}
	if err := config.Validate(); err != nil {
		t.Fatalf("validate provider config: %v", err)
	}
	redacted := config.Redacted()
	if _, exists := redacted["endpoint"]; exists {
		t.Fatal("redacted metadata contains endpoint")
	}
	if _, exists := redacted["credential_ref"]; exists {
		t.Fatal("redacted metadata contains credential reference")
	}
}

func TestConfigRejectsEndpointWithoutCredentialReference(t *testing.T) {
	config := Config{ID: "provider", Model: "model", Endpoint: "https://provider.invalid", Timeout: time.Second, MaxInputBytes: 1, MaxOutputBytes: 1, MaxOutputItems: 1}
	if err := config.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("config error = %v, want invalid config", err)
	}
}
