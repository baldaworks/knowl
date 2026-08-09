// Package provider contains replaceable maintainer-provider boundaries.
package provider

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInvalidConfig = errors.New("invalid Knowl provider configuration")

// Config selects the independent maintainer model and its bounded resources.
type Config struct {
	ID               string
	Model            string
	Endpoint         string
	CredentialRef    string
	ReasoningEnabled bool
	ReasoningEffort  string
	Timeout          time.Duration
	MaxInputBytes    int
	MaxOutputBytes   int
	MaxOutputItems   int
}

// Validate checks provider configuration without contacting a provider.
func (config Config) Validate() error {
	if strings.TrimSpace(config.ID) == "" || strings.TrimSpace(config.Model) == "" {
		return fmt.Errorf("provider id and model are required: %w", ErrInvalidConfig)
	}
	if config.Timeout <= 0 || config.MaxInputBytes <= 0 || config.MaxOutputBytes <= 0 || config.MaxOutputItems <= 0 {
		return fmt.Errorf("provider timeout and output limits must be positive: %w", ErrInvalidConfig)
	}
	if strings.TrimSpace(config.CredentialRef) == "" && strings.TrimSpace(config.Endpoint) != "" {
		return fmt.Errorf("provider endpoint requires a credential reference: %w", ErrInvalidConfig)
	}
	return nil
}

// Redacted returns non-secret metadata safe for diagnostics.
func (config Config) Redacted() map[string]any {
	return map[string]any{
		"id":                    config.ID,
		"model":                 config.Model,
		"reasoning_enabled":     config.ReasoningEnabled,
		"reasoning_effort":      config.ReasoningEffort,
		"timeout":               config.Timeout.String(),
		"max_input_bytes":       config.MaxInputBytes,
		"max_output_bytes":      config.MaxOutputBytes,
		"max_output_items":      config.MaxOutputItems,
		"credential_configured": strings.TrimSpace(config.CredentialRef) != "",
	}
}
