package main

import (
	"encoding/json"
	"errors"
	"github.com/baldaworks/knowl/pkg/knowl"
	"strconv"
	"testing"
)

func TestWorkersConfigReachesHost(t *testing.T) {
	for _, tc := range []struct {
		name, setting string
		want          int
	}{
		{"default", "", 1}, {"one", "workers: 1\n", 1}, {"two", "workers: 2\n", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, err := tryLoadTestConfig(t, testConfigOptions{knowl: "provider: codex\n" + tc.setting})
			if err != nil {
				t.Fatal(err)
			}
			config, err := hostConfig(ctx)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			var actual struct{ Workers int }
			if err := json.Unmarshal(encoded, &actual); err != nil {
				t.Fatal(err)
			}
			if actual.Workers != tc.want {
				t.Fatalf("host workers = %d, want %d", actual.Workers, tc.want)
			}
		})
	}
}

func TestWorkersConfigRejectsUnsupportedValues(t *testing.T) {
	for _, value := range []string{"0", "-1", "3", "1.5", strconv.FormatBool(true), "\"2\""} {
		t.Run(value, func(t *testing.T) {
			if _, err := tryLoadTestConfig(t, testConfigOptions{knowl: "provider: codex\nworkers: " + value + "\n"}); !errors.Is(err, knowl.ErrWorkerConfigInvalid) {
				t.Fatalf("unsupported worker capacity result: %v", err)
			}
		})
	}
}
