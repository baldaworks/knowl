package main

import (
	"errors"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl"
)

func TestHostConfigMapsWebAndStdioClearsIt(t *testing.T) {
	ctx := loadTestConfig(t, testConfigOptions{knowl: "provider: codex\nweb:\n  enabled: true\noperator:\n  token: local-secret\n"})
	config, err := hostConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Web.Enabled || config.OperatorToken != "local-secret" {
		t.Fatal("web mapping lost")
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	config.OperatorToken = " "
	if err := config.Validate(); !errors.Is(err, knowl.ErrWebConfigInvalid) {
		t.Fatalf("invalid enabled config: %v", err)
	}
	config = stdioHostConfig(config)
	if config.Web.Enabled || config.OperatorToken != "" || config.ListenAddr != "127.0.0.1:0" {
		t.Fatal("stdio retained HTTP settings")
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestHostConfigWebDefaultsDisabled(t *testing.T) {
	ctx := loadTestConfig(t, testConfigOptions{knowl: "provider: codex\n"})
	config, err := hostConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if config.Web.Enabled {
		t.Fatal("web enabled by default")
	}
}
