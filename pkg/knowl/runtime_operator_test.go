package knowl

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/provider"
)

func TestWebConfigValidationPrecedesComposition(t *testing.T) {
	config := DefaultConfig()
	config.Workspace = filepath.Join(t.TempDir(), "uncreated")
	config.Web.Enabled = true
	for _, token := range []string{"", " \t\n"} {
		config.OperatorToken = token
		if err := config.Validate(); !errors.Is(err, ErrWebConfigInvalid) {
			t.Fatalf("Validate: %v", err)
		}
		if _, err := New(t.Context(), Options{Config: config}); !errors.Is(err, ErrWebConfigInvalid) {
			t.Fatalf("New: %v", err)
		}
		if _, err := os.Stat(config.Workspace); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("composition wrote workspace: %v", err)
		}
	}
}

func TestOperatorRuntimeEnabledAndDisabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			config := DefaultConfig()
			config.Workspace = t.TempDir()
			config.OperatorToken = operatorRuntimeToken
			config.Web.Enabled = enabled
			config.ListenAddr = operatorRuntimeAddress
			host, err := NewHost(t.Context(), config, provider.Fixture{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = host.Close() })
			if host.listener != nil || host.started || host.Ready() {
				t.Fatal("composition started listener/jobs")
			}
			if host.Operator() == nil {
				t.Fatal("missing embedded read service")
			}
			request := func(path, token string) int {
				r := httptest.NewRequest(http.MethodGet, path, nil)
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				host.Handler().ServeHTTP(w, r)
				return w.Code
			}
			want := 404
			if enabled {
				want = 401
			}
			if got := request("/operator/v1/pages", ""); got != want {
				t.Fatalf("without bearer: %d", got)
			}
			want = 404
			if enabled {
				want = 503
			}
			if got := request("/operator/v1/pages", operatorRuntimeToken); got != want {
				t.Fatalf("before readiness: %d", got)
			}
			if err := host.PrepareReadOnly(); err != nil {
				t.Fatal(err)
			}
			want = 404
			if enabled {
				want = 200
			}
			for _, path := range []string{"/operator/v1/pages", "/operator/v1/operations", "/operator/v1/sources"} {
				if got := request(path, operatorRuntimeToken); got != want {
					t.Fatalf("ready %s: %d", path, got)
				}
			}
			if got := request("/v1/retrieve?query=example", ""); got != 401 {
				t.Fatalf("existing auth: %d", got)
			}
			want = 404
			if enabled {
				want = 401
			}
			if got := request("/ui/fragments/knowledge", ""); got != want {
				t.Fatalf("fragments auth: %d", got)
			}
			want = 404
			if enabled {
				want = 200
			}
			for _, path := range []string{"/ui/knowledge", "/ui/search", "/ui/operations", "/ui/sources", "/ui/assets/knowl-logo.png"} {
				if got := request(path, ""); got != want {
					t.Fatalf("shell %s: %d", path, got)
				}
			}
			if _, err := host.Operator().PageSummaries(t.Context(), app.OperatorListOptions{}); err != nil {
				t.Fatal(err)
			}
			if host.listener != nil || host.started {
				t.Fatal("browse started runtime")
			}
		})
	}
}

const operatorRuntimeToken = "operator-runtime-token"
const operatorRuntimeAddress = "127.0.0.1:0"
