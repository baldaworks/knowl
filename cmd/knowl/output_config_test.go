package main

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
)

func TestOutputConfigPreservesExplicitCorrectionAllowance(t *testing.T) {
	for _, value := range []string{"0", "1"} {
		t.Run(value, func(t *testing.T) {
			ctx, err := tryLoadTestConfig(t, testConfigOptions{knowl: "provider: codex\noutput:\n  max_corrections: " + value + "\n"})
			if err != nil {
				t.Fatalf("load supported output policy: %v", err)
			}
			config, err := hostConfig(ctx)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			var projected struct {
				Output struct {
					MaxCorrections *int `json:"max_corrections"`
				}
			}
			if err := json.Unmarshal(encoded, &projected); err != nil {
				t.Fatal(err)
			}
			want := 0
			if value == "1" {
				want = 1
			}
			if projected.Output.MaxCorrections == nil || *projected.Output.MaxCorrections != want {
				t.Fatalf("explicit allowance lost: %+v", projected.Output)
			}
		})
	}
}

func TestOutputConfigRejectsUnboundedAllowance(t *testing.T) {
	for _, value := range []string{"-1", "2", "1.5", "1.9", "-0.5", strconv.FormatBool(true)} {
		t.Run(value, func(t *testing.T) {
			if _, err := tryLoadTestConfig(t, testConfigOptions{knowl: "provider: codex\noutput:\n  max_corrections: " + value + "\n"}); !errors.Is(err, app.ErrOutputCorrectionInvalid) {
				t.Fatalf("unsupported config accepted: %v", err)
			}
		})
	}
}
