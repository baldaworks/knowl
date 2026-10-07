//go:build integration

package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEmbeddingSidecarMergedComposeContract(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker Compose is required for the deployment contract")
	}

	expectedKnowlEnv := map[string]string{"OPENAI_API_KEY": "test-api-key", "OPENAI_MODEL": "test-model", "KNOWL_OPERATOR_TOKEN": "test-operator-token"}
	for key, value := range expectedKnowlEnv {
		t.Setenv(key, value)
	}
	repo := testRepoRoot(t)
	command := exec.CommandContext(t.Context(), "docker", "compose", "-p", "knowl-embedding-contract", "-f", filepath.Join(repo, "deploy", "sidecar", "compose.yaml"), "-f", filepath.Join(repo, "deploy", "sidecar", "embeddings.compose.yaml"), "config", "--format", "json")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("compose config: %v", err)
	}
	type volume struct {
		Type, Source, Target string
		ReadOnly             bool `json:"read_only"`
	}
	type service struct {
		Image, Platform, Runtime string
		CPUs                     float64 `json:"cpus"`
		Memory                   string  `json:"mem_limit"`
		Command                  []string
		Environment              map[string]string
		Networks                 map[string]json.RawMessage
		Ports, Devices, Gpus     []json.RawMessage
		Volumes                  []volume
		Healthcheck              struct {
			Test        []string
			StartPeriod string `json:"start_period"`
		}
		DependsOn map[string]struct{ Condition string } `json:"depends_on"`
		Deploy    struct {
			Resources struct {
				Reservations struct{ Devices []json.RawMessage }
			}
		}
	}
	var config struct {
		Services map[string]service
		Volumes  map[string]json.RawMessage
	}
	if err := json.Unmarshal(output, &config); err != nil {
		t.Fatal(err)
	}
	tei, present := config.Services["tei"]
	if !present {
		t.Fatal("missing TEI service")
	}
	const image = "ghcr.io/huggingface/text-embeddings-inference:cpu-1.9.0@sha256:bc7ad262695df5b7875b0c9c702deb8e9df3953bdf22c3d6068d9c0429b7b3f3"
	if tei.Image != image || tei.Platform != "linux/amd64" || tei.CPUs != 2 || tei.Memory != "4294967296" || len(tei.Ports) != 0 || len(tei.Devices) != 0 || len(tei.Gpus) != 0 || len(tei.Deploy.Resources.Reservations.Devices) != 0 || tei.Runtime != "" {
		t.Fatalf("CPU-only resource/service contract=%+v", tei)
	}
	expectedEnv := map[string]string{"AUTO_TRUNCATE": "false", "RAYON_NUM_THREADS": "2", "OMP_NUM_THREADS": "2", "MKL_NUM_THREADS": "2"}
	if !reflect.DeepEqual(tei.Environment, expectedEnv) {
		t.Fatalf("server environment=%v", tei.Environment)
	}
	flags := map[string]string{}
	for i := 0; i < len(tei.Command); i++ {
		flag := tei.Command[i]
		if flag == "--json-output" {
			continue
		}
		if i+1 == len(tei.Command) {
			t.Fatal("incomplete server flag")
		}
		i++
		if _, duplicate := flags[flag]; duplicate {
			t.Fatal("duplicate server flag")
		}
		flags[flag] = tei.Command[i]
	}
	expectedFlags := map[string]string{"--model-id": sidecarEmbeddingModel, "--revision": "d128750597153bb5987e10b1c3493a34e5a4502a", "--hostname": "0.0.0.0", "--served-model-name": sidecarEmbeddingModel, "--dtype": "float32", "--pooling": "mean", "--tokenization-workers": "2", "--max-concurrent-requests": "4", "--max-client-batch-size": "16", "--max-batch-requests": "8", "--max-batch-tokens": "8192", "--payload-limit": "65536"}
	if !reflect.DeepEqual(flags, expectedFlags) {
		t.Fatalf("server contract=%v", flags)
	}
	if !reflect.DeepEqual(tei.Healthcheck.Test, []string{"CMD", "curl", "--fail", "--silent", "http://127.0.0.1:80/health"}) || tei.Healthcheck.StartPeriod != "10m0s" {
		t.Fatalf("healthcheck=%+v", tei.Healthcheck)
	}
	if len(tei.Volumes) != 1 || tei.Volumes[0].Type != "volume" || tei.Volumes[0].Source != "tei-model-cache" || tei.Volumes[0].Target != "/data" {
		t.Fatalf("model cache=%v", tei.Volumes)
	}
	if _, exists := config.Volumes["tei-model-cache"]; !exists {
		t.Fatal("missing persistent cache volume")
	}
	knowl := config.Services["knowl"]

	for key, value := range expectedKnowlEnv {
		if knowl.Environment[key] != value {
			t.Fatalf("service environment %s was not preserved", key)
		}
	}
	shared := false
	for network := range knowl.Networks {
		if _, present := tei.Networks[network]; present {
			shared = true
		}
	}
	if !shared || knowl.DependsOn["tei"].Condition != "service_started" {
		t.Fatal("private service network/startup fallback contract lost")
	}
	found := false
	for _, mount := range knowl.Volumes {
		if mount.Target == "/etc/knowl/config.yaml" {
			found = mount.ReadOnly && mount.Source == filepath.Join(repo, "deploy", "sidecar", "embeddings.yaml")
		}
	}
	if !found {
		t.Fatal("typed embedding profile not mounted read-only")
	}
}
