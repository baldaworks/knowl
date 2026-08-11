package knowl

import "testing"

func TestConfigValidateAllowsServiceBindAddress(t *testing.T) {
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.ListenAddr = "0.0.0.0:8080"

	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() error: %v", err)
	}
}

func TestConfigValidateRejectsNamedHostBindAddress(t *testing.T) {
	config := DefaultConfig()
	config.Workspace = t.TempDir()
	config.ListenAddr = "knowl:8080"

	if err := config.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want invalid listen host")
	}
}
