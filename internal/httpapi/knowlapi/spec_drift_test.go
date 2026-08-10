package knowlapi

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type routeKey struct {
	Method string
	Path   string
}

type openAPIDocument struct {
	Paths map[string]openAPIPathItem `yaml:"paths"`
}

type openAPIPathItem struct {
	Delete *openAPIOperation `yaml:"delete"`
	Get    *openAPIOperation `yaml:"get"`
	Head   *openAPIOperation `yaml:"head"`
	Patch  *openAPIOperation `yaml:"patch"`
	Post   *openAPIOperation `yaml:"post"`
	Put    *openAPIOperation `yaml:"put"`
}

type openAPIOperation struct {
	OperationID string `yaml:"operationId"`
}

func TestGeneratedRoutesMatchOpenAPIContract(t *testing.T) {
	t.Parallel()

	specRoutes := loadSpecRoutes(t)
	generatedRoutes := loadGeneratedRoutes(t)

	for route, operationID := range specRoutes {
		handlerName, ok := generatedRoutes[route]
		if !ok {
			t.Errorf("generated server missing route %s %s from OpenAPI contract", route.Method, route.Path)
			continue
		}
		wantHandler := exportedOperationName(operationID)
		if handlerName != wantHandler {
			t.Errorf("generated route %s %s uses handler %q, want %q", route.Method, route.Path, handlerName, wantHandler)
		}
	}

	for route, handlerName := range generatedRoutes {
		if _, ok := specRoutes[route]; ok {
			continue
		}
		t.Errorf("generated server exposes route %s %s with handler %q, but the route is absent from api/openapi/knowl.yaml", route.Method, route.Path, handlerName)
	}
}

func loadSpecRoutes(t *testing.T) map[routeKey]string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "openapi", "knowl.yaml"))
	if err != nil {
		t.Fatalf("read OpenAPI spec: %v", err)
	}

	var document openAPIDocument
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("decode OpenAPI spec: %v", err)
	}

	routes := make(map[routeKey]string)
	for path, item := range document.Paths {
		for method, operation := range item.operations() {
			if operation == nil || operation.OperationID == "" {
				continue
			}
			route := routeKey{Method: method, Path: path}
			if existing, exists := routes[route]; exists {
				t.Fatalf("duplicate OpenAPI operation for %s %s: %q and %q", route.Method, route.Path, existing, operation.OperationID)
			}
			routes[route] = operation.OperationID
		}
	}

	return routes
}

func loadGeneratedRoutes(t *testing.T) map[routeKey]string {
	t.Helper()

	content, err := os.ReadFile("server.gen.go")
	if err != nil {
		t.Fatalf("read generated server bindings: %v", err)
	}

	pattern := regexp.MustCompile(`m\.HandleFunc\("([A-Z]+) "\+options\.BaseURL\+"([^"]+)", wrapper\.([A-Za-z0-9_]+)\)`)
	matches := pattern.FindAllStringSubmatch(string(content), -1)
	if len(matches) == 0 {
		t.Fatal("no generated HandleFunc registrations found in server.gen.go")
	}

	routes := make(map[routeKey]string, len(matches))
	for _, match := range matches {
		route := routeKey{Method: match[1], Path: match[2]}
		if existing, exists := routes[route]; exists {
			t.Fatalf("duplicate generated route %s %s for handlers %q and %q", route.Method, route.Path, existing, match[3])
		}
		routes[route] = match[3]
	}

	return routes
}

func (item openAPIPathItem) operations() map[string]*openAPIOperation {
	return map[string]*openAPIOperation{
		"DELETE": item.Delete,
		"GET":    item.Get,
		"HEAD":   item.Head,
		"PATCH":  item.Patch,
		"POST":   item.Post,
		"PUT":    item.Put,
	}
}

func exportedOperationName(operationID string) string {
	if operationID == "" {
		return ""
	}
	return strings.ToUpper(operationID[:1]) + operationID[1:]
}
