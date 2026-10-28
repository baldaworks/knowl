package main

import (
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl"
)

func TestHostConfigMapsFacadePoliciesAndStdioClearsHTTP(t *testing.T) {
	ctx := loadTestConfig(t, testConfigOptions{knowl: "provider: codex\napi:\n  auth_mode: token\n  access_mode: read_write\nmcp:\n  auth_mode: token\n  access_mode: read_only\nweb:\n  enabled: true\n  auth_mode: public\n  access_mode: read_only\noperator:\n  token: local-secret\n"})
	config, err := hostConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Web.Enabled || config.Web.AuthMode != knowl.AuthModePublic || config.API.AccessMode != knowl.AccessModeReadWrite || config.MCP.AccessMode != knowl.AccessModeReadOnly || config.OperatorToken != "local-secret" {
		t.Fatalf("facade mapping lost: API %+v MCP %+v Web %+v", config.API, config.MCP, config.Web)
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
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
