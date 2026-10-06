package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/rs/zerolog"
	zerologlog "github.com/rs/zerolog/log"
	"gopkg.in/yaml.v3"
)

const operatorTestToken = "operator-token-canary"
const operatorTestPage = "concepts/example.md"
const operatorTestSnapshot = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type operatorFixture struct {
	err      error
	calls    int
	scope    domain.ScopeRef
	snapshot string
}

func (f *operatorFixture) check(scope domain.ScopeRef) error {
	f.calls++
	f.scope = scope
	return f.err
}
func (f *operatorFixture) CatalogChildren(_ context.Context, scope domain.ScopeRef, _ domain.PageID, _ app.OperatorReadOptions) (app.OperatorCatalogRead, error) {
	return app.OperatorCatalogRead{Parent: domain.OperatorCatalogSummary{ID: "index.md", Title: "Knowledge"}, Children: app.OperatorReadPage[domain.OperatorCatalogChild]{Items: []domain.OperatorCatalogChild{}, SnapshotVersion: f.snapshot}}, f.check(scope)
}
func (f *operatorFixture) PageSummaries(_ context.Context, scope domain.ScopeRef, _ app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorPageSummary], error) {
	return app.OperatorReadPage[domain.OperatorPageSummary]{Items: []domain.OperatorPageSummary{{ID: operatorTestPage, Title: "Example", Digest: operatorTestSnapshot, Version: operatorTestSnapshot, UpdatedAt: time.Unix(1, 0).UTC()}}, NextKey: operatorTestPage, SnapshotVersion: f.snapshot}, f.check(scope)
}
func (f *operatorFixture) Page(_ context.Context, scope domain.ScopeRef, id domain.PageID, _ domain.ReadLimits) (domain.OperatorPage, error) {
	return domain.OperatorPage{ID: id, Title: "Example", Markdown: "# Example", Digest: operatorTestSnapshot, Version: operatorTestSnapshot, RelatedPageIDs: []domain.PageID{}, Sources: []domain.OperatorPageSource{}}, f.check(scope)
}
func (f *operatorFixture) SourceRevision(_ context.Context, scope domain.ScopeRef, ref string, _ domain.ReadLimits) (domain.OperatorSourceRevision, error) {
	return domain.OperatorSourceRevision{SourceRef: ref, Source: domain.SourceRef{Adapter: "git", ID: "doc"}, Version: domain.SourceVersion{Version: "one", Digest: operatorTestSnapshot}, MediaType: "text/plain", Digest: operatorTestSnapshot, Text: "immutable text"}, f.check(scope)
}
func (f *operatorFixture) ListSources(_ context.Context, scope domain.ScopeRef, _ app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorSourceSummary], error) {
	return app.OperatorReadPage[domain.OperatorSourceSummary]{Items: []domain.OperatorSourceSummary{{ID: "source", Type: domain.SourceTypeFilesystem, Enabled: true}}}, f.check(scope)
}
func (f *operatorFixture) Source(_ context.Context, scope domain.ScopeRef, id domain.SourceID) (domain.OperatorSourceSummary, error) {
	return domain.OperatorSourceSummary{ID: id, Type: domain.SourceTypeFilesystem, Enabled: true}, f.check(scope)
}
func (f *operatorFixture) ListSourceDocuments(_ context.Context, scope domain.ScopeRef, _ domain.SourceID, _ app.OperatorReadOptions) (app.OperatorReadPage[domain.OperatorDocumentSummary], error) {
	return app.OperatorReadPage[domain.OperatorDocumentSummary]{Items: []domain.OperatorDocumentSummary{}}, f.check(scope)
}
func (f *operatorFixture) ListOperations(_ context.Context, scope domain.ScopeRef, _ app.OperatorOperationReadOptions) (app.OperatorReadPage[domain.OperatorOperationSummary], error) {
	return app.OperatorReadPage[domain.OperatorOperationSummary]{Items: []domain.OperatorOperationSummary{}}, f.check(scope)
}
func operatorTestHandler(t *testing.T, f *operatorFixture, ready func() bool) http.Handler {
	t.Helper()
	service, err := app.NewOperatorService("trusted", app.OperatorReaders{Catalogs: f, Pages: f, Page: f, Revisions: f, Sources: f, Documents: f, Operations: f}, app.OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return WithOperatorAuth(NewOperatorHandler(service, ready), operatorTestToken)
}
func operatorRequest(handler http.Handler, path, body, token string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
func assertOperatorError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != status || body.Error != code {
		t.Fatalf("response = %d %+v; want %d %s", response.Code, body, status, code)
	}
	if response.Header().Get("Cache-Control") != operatorTestNoStore {
		t.Fatal("missing no-store")
	}
}

func TestOperatorInputBoundary(t *testing.T) {
	tests := []struct {
		name, path, body, token string
		headers                 map[string]string
		status                  int
		code                    string
	}{
		{name: "unauthenticated", path: operatorTestPagesRoute, status: 401, code: "unauthorized"},
		{name: "wrong bearer", path: operatorTestPagesRoute, token: "wrong", status: 401, code: "unauthorized"},
		{name: "blank scope", path: "/operator/v1/pages?scope=", status: 403, code: operatorTestScopeForbidden, token: operatorTestToken},
		{name: "workspace", path: "/operator/v1/pages?workspace_root=", status: 403, code: operatorTestScopeForbidden, token: operatorTestToken},
		{name: "scope header", path: operatorTestPagesRoute, headers: map[string]string{"X-Knowl-Scope": ""}, status: 403, code: operatorTestScopeForbidden, token: operatorTestToken},
		{name: "workspace header", path: operatorTestPagesRoute, headers: map[string]string{"X-Workspace-Path": "/outside"}, status: 403, code: operatorTestScopeForbidden, token: operatorTestToken},
		{name: "json override", path: operatorTestPagesRoute, body: `{"scope":""}`, status: 403, code: operatorTestScopeForbidden, token: operatorTestToken},
		{name: "form override", path: operatorTestPagesRoute, body: "workspace=", status: 403, code: operatorTestScopeForbidden, token: operatorTestToken},
		{name: "get body", path: operatorTestPagesRoute, body: `{}`, status: 400, code: operatorTestInvalidRequest, token: operatorTestToken},
		{name: "large body", path: operatorTestPagesRoute, body: strings.Repeat("x", maxOperatorBodyBytes+1), status: 413, code: operatorTestReadLimit, token: operatorTestToken},
		{name: "unknown", path: "/operator/v1/pages?other=secret", status: 400, code: operatorTestInvalidRequest, token: operatorTestToken},
		{name: "duplicate", path: "/operator/v1/pages?limit=1&limit=2", status: 400, code: operatorTestInvalidRequest, token: operatorTestToken},
		{name: "malformed query", path: "/operator/v1/pages?x=%zz", status: 400, code: operatorTestInvalidRequest, token: operatorTestToken},
		{name: "large query", path: "/operator/v1/pages?x=" + strings.Repeat("x", maxOperatorQueryBytes), status: 413, code: operatorTestReadLimit, token: operatorTestToken},
		{name: "limit zero", path: "/operator/v1/pages?limit=0", status: 400, code: operatorTestLimitInvalid, token: operatorTestToken},
		{name: "limit large", path: "/operator/v1/pages?limit=101", status: 400, code: operatorTestLimitInvalid, token: operatorTestToken},
		{name: "limit malformed", path: "/operator/v1/pages?limit=no", status: 400, code: operatorTestLimitInvalid, token: operatorTestToken},
		{name: "cursor large", path: "/operator/v1/pages?cursor=" + strings.Repeat("x", 8193), status: 400, code: operatorTestCursorInvalid, token: operatorTestToken},
		{name: "cursor tampered", path: "/operator/v1/pages?cursor=abc", status: 400, code: operatorTestCursorInvalid, token: operatorTestToken},
		{name: "page required", path: operatorTestPageRoute, status: 400, code: operatorTestInvalidRequest, token: operatorTestToken},
		{name: "page traversal", path: "/operator/v1/page?page_id=../private", status: 400, code: operatorTestInvalidRequest, token: operatorTestToken},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := &operatorFixture{snapshot: operatorTestSnapshot}
			handler := operatorTestHandler(t, fixture, func() bool { return true })
			assertOperatorError(t, operatorRequest(handler, test.path, test.body, test.token, test.headers), test.status, test.code)
			if fixture.calls != 0 {
				t.Fatalf("boundary dispatched %d reads", fixture.calls)
			}
		})
	}
}

func TestOperatorErrorsAndSnapshot(t *testing.T) {
	fixture := &operatorFixture{snapshot: operatorTestSnapshot}
	ready := true
	handler := operatorTestHandler(t, fixture, func() bool { return ready })
	first := operatorRequest(handler, "/operator/v1/pages?limit=1", "", operatorTestToken, nil)
	var page domain.OperatorList[domain.OperatorPageSummary]
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if first.Code != 200 || page.NextCursor == "" || fixture.scope != "trusted" {
		t.Fatalf("first page: %d %+v", first.Code, page)
	}
	fixture.snapshot = strings.Repeat("b", 64)
	assertOperatorError(t, operatorRequest(handler, "/operator/v1/pages?limit=1&cursor="+url.QueryEscape(page.NextCursor), "", operatorTestToken, nil), 409, "snapshot_changed")
	ready = false
	assertOperatorError(t, operatorRequest(handler, operatorTestPagesRoute, "", operatorTestToken, nil), 503, "not_ready")
	ready = true
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{app.ErrPageNotFound, 404, "page_not_found"}, {app.ErrOperatorCatalogNotFound, 404, "catalog_not_found"},
		{app.ErrSourceNotFound, 404, "source_not_found"}, {app.ErrOperatorSourceRevisionNotFound, 404, "source_revision_not_found"},
		{app.ErrOperatorReadLimitExceeded, 413, operatorTestReadLimit}, {app.ErrOperatorUnsupportedFormat, 415, "unsupported_format"},
		{app.ErrOperatorCapabilityUnavailable, 503, "capability_unavailable"}, {app.ErrOperatorWorkspaceUnavailable, 503, operatorTestWorkspaceUnavailable},
		{errors.New("provider secret-canary /private/path"), 500, "internal_error"},
	} {
		fixture.err = test.err
		assertOperatorError(t, operatorRequest(handler, operatorTestPagesRoute, "", operatorTestToken, nil), test.status, test.code)
	}
}

func TestOperatorOptionalCapabilities(t *testing.T) {
	fixture := &operatorFixture{snapshot: operatorTestSnapshot}
	service, err := app.NewOperatorService("trusted", app.OperatorReaders{Pages: fixture}, app.OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewOperatorHandler(service, nil)
	if response := operatorRequest(handler, operatorTestPagesRoute, "", "", nil); response.Code != 200 {
		t.Fatalf("available page capability: %d", response.Code)
	}
	assertOperatorError(t, operatorRequest(handler, "/operator/v1/operations", "", "", nil), 503, "capability_unavailable")
}

func TestOperatorDoesNotLogRequestSecrets(t *testing.T) {
	var logs bytes.Buffer
	previousZero := zerologlog.Logger
	zerologlog.Logger = zerolog.New(&logs)
	t.Cleanup(func() { zerologlog.Logger = previousZero })
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	fixture := &operatorFixture{snapshot: operatorTestSnapshot, err: errors.New("document-canary")}
	handler := operatorTestHandler(t, fixture, nil)
	_ = operatorRequest(handler, "/operator/v1/page?page_id=concepts/query-canary.md", "", operatorTestToken, nil)
	_ = operatorRequest(handler, operatorTestPagesRoute, `{"document":"body-canary"}`, operatorTestToken, nil)
	decoder := json.NewDecoder(&logs)
	for {
		var record map[string]any
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("operator emitted unexpected access/error record: %+v", record)
	}
}

type operatorContract struct {
	Paths map[string]struct {
		Get struct {
			OperationID string `yaml:"operationId"`
			Responses   map[string]struct {
				Content map[string]struct {
					Schema struct {
						Ref string `yaml:"$ref"`
					} `yaml:"schema"`
				} `yaml:"content"`
			} `yaml:"responses"`
		} `yaml:"get"`
	} `yaml:"paths"`
	Components struct {
		Schemas map[string]struct {
			Properties map[string]any `yaml:"properties"`
			Required   []string       `yaml:"required"`
		} `yaml:"schemas"`
	} `yaml:"components"`
}

func TestOperatorLiveRoutesMatchParsedContract(t *testing.T) {
	data, err := os.ReadFile("../../../api/openapi/operator.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var contract operatorContract
	if err := yaml.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	routes := map[string]string{"/operator/v1/catalogs": "", operatorTestPagesRoute: "", operatorTestPageRoute: "?page_id=" + operatorTestPage, "/operator/v1/source-revision": "?source_ref=git:doc@one", "/operator/v1/sources": "", "/operator/v1/sources/{source_id}": "", "/operator/v1/operations": ""}
	if len(contract.Paths) != len(routes) {
		t.Fatalf("contract routes: %d", len(contract.Paths))
	}
	fixture := &operatorFixture{snapshot: operatorTestSnapshot}
	handler := operatorTestHandler(t, fixture, nil)
	for path, query := range routes {
		t.Run(path, func(t *testing.T) {
			operation, ok := contract.Paths[path]
			if !ok || operation.Get.OperationID == "" {
				t.Fatal("missing GET contract")
			}
			live := strings.ReplaceAll(path, "{source_id}", "source") + query
			response := operatorRequest(handler, live, "", operatorTestToken, nil)
			if response.Code != 200 || response.Header().Get("Cache-Control") != operatorTestNoStore {
				t.Fatalf("live response %d", response.Code)
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			schemaName := strings.TrimPrefix(operation.Get.Responses["200"].Content["application/json"].Schema.Ref, "#/components/schemas/")
			schema, ok := contract.Components.Schemas[schemaName]
			if !ok {
				t.Fatal("missing response schema")
			}
			for key := range body {
				if _, ok := schema.Properties[key]; !ok {
					t.Fatalf("uncontracted field %s", key)
				}
			}
			for _, key := range schema.Required {
				if _, ok := body[key]; !ok {
					t.Fatalf("missing required field %s", key)
				}
			}
			request := httptest.NewRequest(http.MethodPost, live, nil)
			request.Header.Set("Authorization", "Bearer "+operatorTestToken)
			denied := httptest.NewRecorder()
			handler.ServeHTTP(denied, request)
			assertOperatorError(t, denied, 405, "method_not_allowed")
		})
	}
	unknown := operatorRequest(handler, "/operator/v1/unregistered", "", operatorTestToken, nil)
	assertOperatorError(t, unknown, 404, "not_found")
	// Optional JSON fields retain the application's safe projection without remapping.
	if fixture.scope != "trusted" {
		t.Fatal("caller changed scope")
	}
}

const (
	operatorTestPagesRoute           = "/operator/v1/pages"
	operatorTestPageRoute            = "/operator/v1/page"
	operatorTestNoStore              = "no-store"
	operatorTestScopeForbidden       = "scope_override_forbidden"
	operatorTestInvalidRequest       = "invalid_request"
	operatorTestReadLimit            = "read_limit_exceeded"
	operatorTestLimitInvalid         = "limit_invalid"
	operatorTestCursorInvalid        = "cursor_invalid"
	operatorTestWorkspaceUnavailable = "workspace_unavailable"
)
