package operatorapi

import (
	"net/http"
	"os"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

type routeRecorder struct{ routes map[string]bool }

func (r *routeRecorder) HandleFunc(pattern string, _ func(http.ResponseWriter, *http.Request)) {
	r.routes[pattern] = true
}
func (*routeRecorder) ServeHTTP(http.ResponseWriter, *http.Request) {}

func TestGeneratedRegistrationsMatchParsedSpec(t *testing.T) {
	data, err := os.ReadFile("../../../api/openapi/operator.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string `yaml:"operationId"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for path, methods := range spec.Paths {
		for method, operation := range methods {
			if method != "get" || operation.OperationID == "" {
				t.Fatalf("unexpected operation %s %s", method, path)
			}
			want["GET "+path] = true
		}
	}
	recorder := &routeRecorder{routes: map[string]bool{}}
	HandlerWithOptions(nil, StdHTTPServerOptions{BaseRouter: recorder})
	if len(want) != 7 || !reflect.DeepEqual(recorder.routes, want) {
		t.Fatalf("generated routes = %v; contract = %v", recorder.routes, want)
	}
}
