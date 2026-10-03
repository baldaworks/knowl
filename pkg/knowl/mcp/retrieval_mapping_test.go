package mcp

import (
	"encoding/json"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"reflect"
	"testing"
)

func TestRetrievalTransportPublishesOnlySafeStatus(t *testing.T) {
	report := &knowl.RetrievalReport{Requested: knowl.RetrievalHybrid, Effective: knowl.RetrievalDegraded, Reason: knowl.RetrievalUnavailable, ModelSpace: "0123456789abcdef", ScannedChunks: 42}
	operation := knowl.Operation{Retrieval: report}
	values := []any{retrieveResult(app.QueryResult{Retrieval: report}), operationResult(operation)}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(encoded, &parsed); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"effective": "degraded", "reason": "unavailable"}
		if !reflect.DeepEqual(parsed["retrieval"], want) {
			t.Fatalf("retrieval=%#v", parsed["retrieval"])
		}
	}
	legacy, err := json.Marshal(operationResult(knowl.Operation{}))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(legacy, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, present := parsed["retrieval"]; present {
		t.Fatal("invented legacy report")
	}
}
