package knowl_test

import (
	"context"
	"encoding/json"
	"errors"
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
	baselineHTTP   = "http"
	baselineMCP    = "mcp"
	baselineURIKey = "uri"
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
		for _, suppliedContent := range []bool{false, true} {
			name := transport
			if suppliedContent {
				name += "-content-origin"
			}
			t.Run(name, func(t *testing.T) {
				var fetches atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					fetches.Add(1)
					w.WriteHeader(http.StatusNoContent)
				}))
				defer server.Close()
				uri := server.URL + "/source"
				arguments := map[string]string{baselineURIKey: uri}
				wantText, wantMediaType, wantAdapter := uri, "text/uri-list", "uri"
				if suppliedContent {
					wantText, wantMediaType, wantAdapter = "# Decision\n\nThe caller supplied the document text.", "text/plain", "inline"
					arguments = map[string]string{hostSourceContentKey: wantText, "origin": uri}
				}
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
				result := submitBaselineSource(t, host, transport, arguments)
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				var input domain.MaintenanceInput
				select {
				case input = <-maintainer.inputs:
				case <-ctx.Done():
					t.Fatal("URI ingest did not reach real provider boundary")
				}
				waitForGoldenOperation(t, &goldenMCPDriver{host: host}, result.OperationID)
				raw, err := workspace.ReadSource(ctx, input.Source, app.DefaultReadLimits())
				if err != nil {
					t.Fatal(err)
				}
				if input.Source.MediaType != wantMediaType || input.Source.Source.Adapter != wantAdapter || input.Source.Source.ID != uri || input.SourceText != wantText || string(raw) != wantText || fetches.Load() != 0 {
					t.Fatal("ingest changed accepted content/identity or fetched the reference/origin")
				}
				replay := submitBaselineSource(t, host, transport, arguments)
				if replay.OperationID != result.OperationID || replay.Status != hostCompletedStatus {
					t.Fatalf("terminal replay=%+v", replay)
				}
				select {
				case <-maintainer.inputs:
					t.Fatal("terminal replay invoked maintenance again")
				default:
				}
				if fetches.Load() != 0 {
					t.Fatal("terminal replay fetched the reference/origin")
				}
				if !suppliedContent {
					t.Logf(`{"case_id":"uri-reference-%s","reference_preserved":true,"fetches":0,"outcome":"met"}`, transport)
				}
			})
		}
	}
}

// The two transports must preserve the same accepted payload and replay identity.
func submitBaselineSource(t *testing.T, host *knowl.Host, transport string, arguments map[string]string) mcp.IngestResult {
	t.Helper()
	if transport == baselineMCP {
		values := make(map[string]any, len(arguments))
		for k, v := range arguments {
			values[k] = v
		}
		value, err := host.MCP().Call(t.Context(), hostIngestToolName, values)
		if err != nil {
			t.Fatal(err)
		}
		result, ok := value.(mcp.IngestResult)
		if !ok {
			t.Fatalf("ingest type=%T", value)
		}
		return result
	}
	payload, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	body, status, err := doHostRequest(t, host, http.MethodPost, "/v1/ingest", payload)
	if err != nil || status != http.StatusOK {
		t.Fatalf("HTTP ingest status=%d err=%v", status, err)
	}
	var result mcp.IngestResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPublicIngestRequiresExactlyOnePayload(t *testing.T) {
	for _, transport := range []string{baselineHTTP, baselineMCP} {
		t.Run(transport, func(t *testing.T) {
			_, config := newGoldenWorkspace(t)
			maintainer := &uriBaselineMaintainer{inputs: make(chan domain.MaintenanceInput, 1)}
			host, err := knowl.NewHost(t.Context(), config, maintainer)
			if err != nil {
				t.Fatal(err)
			}
			defer shutdownHost(t, host)
			if err := host.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, args := range []map[string]string{{}, {hostSourceContentKey: "supplied text", baselineURIKey: "https://example.invalid/source"}} {
				if transport == baselineMCP {
					values := make(map[string]any, len(args))
					for k, v := range args {
						values[k] = v
					}
					if _, err := host.MCP().Call(t.Context(), hostIngestToolName, values); !errors.Is(err, mcp.ErrInvalidArguments) {
						t.Fatalf("MCP invalid payload error=%v", err)
					}
				} else {
					payload, err := json.Marshal(args)
					if err != nil {
						t.Fatal(err)
					}
					body, status, err := doHostRequest(t, host, http.MethodPost, "/v1/ingest", payload)
					if err != nil || status != http.StatusBadRequest {
						t.Fatalf("HTTP invalid payload status=%d err=%v", status, err)
					}
					var response struct {
						Error string `json:"error"`
					}
					if err := json.Unmarshal(body, &response); err != nil {
						t.Fatal(err)
					}
					if response.Error != "invalid_request" {
						t.Fatalf("HTTP error code=%s", response.Error)
					}
				}
			}
		})
	}
}
