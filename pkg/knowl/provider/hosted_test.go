package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/agentfactory"
)

func TestRuntimeMaintainerUsesHostedAgentThroughRequestGuard(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Errorf("unexpected hosted request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var request struct {
			Model    string                           `json:"model"`
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Model != "fixture-model" || len(request.Messages) == 0 {
			t.Errorf("unexpected model or empty conversation: %s", request.Model)
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{
			"role": "assistant", "content": `{"schema_digest":"schema","source_refs":["fixture:source@1"],"edits":[{"path":"wiki/concepts/fixture.md","content":"# Fixture\n"}]}`,
		}}}}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	t.Setenv("OPENAI_BASE_URL", server.URL)
	factory := agentfactory.New(map[string]agentconfig.Config{
		"hosted": {Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{APIKey: "fixture-key", Model: "fixture-model"}},
	}, nil)
	maintainer, err := NewRuntimeMaintainer(factory, "hosted", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := maintainer.Close(); err != nil {
			t.Error(err)
		}
	})
	for range 2 {
		plan, err := maintainer.Plan(t.Context(), testMaintenanceInput())
		if err != nil {
			t.Fatalf("hosted maintenance: %v", err)
		}
		if plan.SchemaDigest != testMaintenanceInput().Schema.Digest || len(plan.Edits) != 1 || plan.Edits[0].Path != "wiki/concepts/fixture.md" || string(plan.Edits[0].Content) != "# Fixture\n" || len(plan.SourceRefs) != 1 || plan.SourceRefs[0] != "fixture:source@1" {
			t.Fatalf("hosted plan was not preserved: %+v", plan)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("hosted requests = %d, want 2", calls.Load())
	}
}
