package app

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestMaintenanceDiagnosticsNormalizeAndRoundTrip(t *testing.T) {
	t.Parallel()
	input := []knowl.MaintenanceDiagnostic{
		{Code: knowl.DiagnosticOriginalLinkUnresolved, Path: "wiki/entities/two.md", Target: "entities/missing"},
		{Code: knowl.DiagnosticCitationUnknownSource, Path: lintPageOnePath},
		{Code: knowl.DiagnosticCitationUnknownSource, Path: lintPageOnePath},
	}
	want := []knowl.MaintenanceDiagnostic{
		{Code: knowl.DiagnosticCitationUnknownSource, Path: lintPageOnePath},
		{Code: knowl.DiagnosticOriginalLinkUnresolved, Path: "wiki/entities/two.md", Target: "entities/missing"},
	}
	encoded, err := EncodeMaintenanceDiagnostics(input)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeMaintenanceDiagnostics(encoded)
	if err != nil || !reflect.DeepEqual(decoded, want) {
		t.Fatalf("diagnostic round trip = %#v, %v", decoded, err)
	}
}

func TestMaintenanceDiagnosticsFailClosedAtBounds(t *testing.T) {
	t.Parallel()
	invalid := [][]knowl.MaintenanceDiagnostic{
		{{Code: "bad code", Path: "wiki/page.md"}},
		{{Code: knowl.DiagnosticCitationUnknownSource, Path: "../secret"}},
		{{Code: knowl.DiagnosticOriginalLinkUnresolved, Path: "wiki/page.md", Target: "https://secret.example/path"}},
		{{Code: knowl.DiagnosticCitationUnknownSource, Path: strings.Repeat("x", maxMaintenanceDiagnosticPath+1)}},
	}
	tooMany := make([]knowl.MaintenanceDiagnostic, maxMaintenanceDiagnostics+1)
	for index := range tooMany {
		tooMany[index] = knowl.MaintenanceDiagnostic{Code: knowl.DiagnosticCitationUnknownSource, Path: fmt.Sprintf("wiki/%03d.md", index)}
	}
	invalid = append(invalid, tooMany)
	tooLarge := make([]knowl.MaintenanceDiagnostic, maxMaintenanceDiagnostics)
	for index := range tooLarge {
		tooLarge[index] = knowl.MaintenanceDiagnostic{Code: knowl.DiagnosticCitationUnknownSource, Path: fmt.Sprintf("wiki/%03d-%s.md", index, strings.Repeat("x", 600))}
	}
	invalid = append(invalid, tooLarge)
	for _, diagnostics := range invalid {
		if _, err := NormalizeMaintenanceDiagnostics(diagnostics); !errors.Is(err, ErrMaintenanceDiagnosticInvalid) {
			t.Fatalf("NormalizeMaintenanceDiagnostics() error = %v", err)
		}
	}
	for _, payload := range []string{`{}`, `[{"code":"citation.unknown_source","path":"wiki/page.md","extra":true}]`, `[{"path":"wiki/page.md","code":"citation.unknown_source"}]`, `[] trailing`} {
		if _, err := DecodeMaintenanceDiagnostics(payload); !errors.Is(err, ErrMaintenanceDiagnosticInvalid) {
			t.Fatalf("DecodeMaintenanceDiagnostics(%q) error = %v", payload, err)
		}
	}
}
