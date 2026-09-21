package main

import (
	"errors"
	"testing"

	"github.com/baldaworks/knowl/internal/releaseinfo"
)

func TestVersionCommand(t *testing.T) {
	tests := []struct {
		name       string
		version    string
		wantOutput string
		wantErr    error
	}{
		{name: "development", version: "dev", wantOutput: "{\"version\":\"dev\",\"release\":false}\n"},
		{name: "release version", version: "0.6.0", wantOutput: "{\"version\":\"0.6.0\",\"tag\":\"v0.6.0\",\"release\":true}\n"},
		{name: "release tag", version: "v0.6.0", wantOutput: "{\"version\":\"0.6.0\",\"tag\":\"v0.6.0\",\"release\":true}\n"},
		{name: "invalid", version: "next", wantErr: releaseinfo.ErrInvalidVersion},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			original := releaseinfo.Version
			releaseinfo.Version = test.version
			t.Cleanup(func() { releaseinfo.Version = original })

			stdout, stderr, err := executeCLICommand(newRootCommand(), []string{versionCommandName, "--" + versionJSONFlagName}, nil)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("version error = %v, want %v", err, test.wantErr)
				}
				if stdout != "" {
					t.Fatalf("version stdout = %q, want empty", stdout)
				}
				return
			}
			if err != nil {
				t.Fatalf("version error: %v; stderr=%s", err, stderr)
			}
			if stdout != test.wantOutput {
				t.Fatalf("version stdout = %q, want %q", stdout, test.wantOutput)
			}
		})
	}
}

func TestVersionHelpDocumentsJSONContract(t *testing.T) {
	flag := newVersionCommand().Flags().Lookup(versionJSONFlagName)
	if flag == nil || flag.DefValue != "false" {
		t.Fatalf("version JSON flag = %#v, want optional boolean flag", flag)
	}
}
