package knowl_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/mcp"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	baselineHTTP = "http"
	baselineMCP  = "mcp"
)

type uriBaselineMaintainer struct{ inputs chan domain.MaintenanceInput }

func (m *uriBaselineMaintainer) Plan(ctx context.Context, input domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	select {
	case m.inputs <- input:
	case <-ctx.Done():
		return domain.ModelEditPlan{}, ctx.Err()
	}
	return domain.ModelEditPlan{SchemaDigest: input.Schema.Digest, SourceRefs: []string{app.SourceRefKey(input.Source)}}, nil
}

func TestContextBaselineURIReferenceThroughHTTPAndMCP(t *testing.T) {
	for _, transport := range []string{baselineHTTP, baselineMCP} {
		t.Run(transport, func(t *testing.T) {
			var fetches atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fetches.Add(1)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			uri := server.URL + "/source"
			workspace, config := newGoldenWorkspace(t)
			maintainer := &uriBaselineMaintainer{inputs: make(chan domain.MaintenanceInput, 1)}
			host, err := knowl.NewHost(t.Context(), config, maintainer)
			if err != nil {
				t.Fatal(err)
			}
			defer shutdownHost(t, host)
			if err := host.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			var id domain.OperationID
			if transport == baselineMCP {
				value, callErr := host.MCP().Call(t.Context(), hostIngestToolName, map[string]any{"uri": uri})
				if callErr != nil {
					t.Fatal(callErr)
				}
				result, ok := value.(mcp.IngestResult)
				if !ok {
					t.Fatalf("ingest type = %T", value)
				}
				id = result.OperationID
			} else {
				payload, marshalErr := json.Marshal(map[string]string{"uri": uri})
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				body, status, requestErr := doHostRequest(t, host, http.MethodPost, "/v1/ingest", payload)
				if requestErr != nil || status != http.StatusOK {
					t.Fatalf("HTTP ingest status=%d error=%v", status, requestErr)
				}
				var response struct {
					ID domain.OperationID `json:"operation_id"`
				}
				if err := json.Unmarshal(body, &response); err != nil {
					t.Fatal(err)
				}
				id = response.ID
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var input domain.MaintenanceInput
			select {
			case input = <-maintainer.inputs:
			case <-ctx.Done():
				t.Fatal("URI ingest did not reach real provider boundary")
			}
			waitForGoldenOperation(t, &goldenMCPDriver{host: host}, id)
			raw, err := workspace.ReadSource(ctx, input.Source, app.DefaultReadLimits())
			if err != nil {
				t.Fatal(err)
			}
			if input.Source.MediaType != "text/uri-list" || input.SourceText != uri || string(raw) != uri || fetches.Load() != 0 {
				t.Fatal("URI ingest did not preserve a reference without fetching")
			}
			t.Logf(`{"case_id":"uri-reference-%s","reference_preserved":true,"fetches":0,"outcome":"met"}`, transport)
		})
	}
}
