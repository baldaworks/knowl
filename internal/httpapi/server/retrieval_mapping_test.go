package server

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestRetrievalTransportPublishesOnlySafeStatus(t *testing.T) {
	report := &domain.RetrievalReport{Requested: domain.RetrievalHybrid, Effective: domain.RetrievalDegraded, Reason: domain.RetrievalUnavailable, ModelSpace: "0123456789abcdef", ScannedChunks: 42}
	operation := domain.Operation{Retrieval: report}
	values := []any{app.PublicRetrieveResult{Retrieval: app.PublicRetrievalStatus(report)}, httpOperationResult(operation), httpIngestResult(operation)}
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
	legacy, err := json.Marshal(httpOperationResult(domain.Operation{}))
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
