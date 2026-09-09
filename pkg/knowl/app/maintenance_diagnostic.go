package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	maxMaintenanceDiagnostics    = 64
	maxMaintenanceDiagnosticPath = 2048
	maxMaintenanceDiagnosticJSON = 32 << 10
)

var ErrMaintenanceDiagnosticInvalid = errors.New("invalid maintenance diagnostic")

// NormalizeMaintenanceDiagnostics validates, de-duplicates, sorts, and copies
// the bounded redacted diagnostics attached to one maintenance operation.
func NormalizeMaintenanceDiagnostics(input []knowl.MaintenanceDiagnostic) ([]knowl.MaintenanceDiagnostic, error) {
	if len(input) > maxMaintenanceDiagnostics {
		return nil, ErrMaintenanceDiagnosticInvalid
	}
	seen := make(map[knowl.MaintenanceDiagnostic]struct{}, len(input))
	for _, diagnostic := range input {
		if diagnostic.Code == "" || !validFailureClass(diagnostic.Code) || !validDiagnosticIdentifier(diagnostic.Path, false) ||
			!validDiagnosticIdentifier(diagnostic.Target, true) {
			return nil, ErrMaintenanceDiagnosticInvalid
		}
		seen[diagnostic] = struct{}{}
	}
	result := make([]knowl.MaintenanceDiagnostic, 0, len(seen))
	for diagnostic := range seen {
		result = append(result, diagnostic)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Path != result[right].Path {
			return result[left].Path < result[right].Path
		}
		if result[left].Code != result[right].Code {
			return result[left].Code < result[right].Code
		}
		return result[left].Target < result[right].Target
	})
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > maxMaintenanceDiagnosticJSON {
		return nil, ErrMaintenanceDiagnosticInvalid
	}
	return result, nil
}

// EncodeMaintenanceDiagnostics returns the canonical bounded persistence form.
func EncodeMaintenanceDiagnostics(input []knowl.MaintenanceDiagnostic) (string, error) {
	normalized, err := NormalizeMaintenanceDiagnostics(input)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", ErrMaintenanceDiagnosticInvalid
	}
	return string(encoded), nil
}

// DecodeMaintenanceDiagnostics fails closed on malformed or non-canonical
// durable payloads.
func DecodeMaintenanceDiagnostics(payload string) ([]knowl.MaintenanceDiagnostic, error) {
	if payload == "" {
		return make([]knowl.MaintenanceDiagnostic, 0), nil
	}
	if len(payload) > maxMaintenanceDiagnosticJSON {
		return nil, ErrMaintenanceDiagnosticInvalid
	}
	var decoded []knowl.MaintenanceDiagnostic
	decoder := json.NewDecoder(bytes.NewBufferString(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, ErrMaintenanceDiagnosticInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, ErrMaintenanceDiagnosticInvalid
	}
	normalized, err := NormalizeMaintenanceDiagnostics(decoded)
	if err != nil {
		return nil, err
	}
	canonical, _ := json.Marshal(normalized)
	if string(canonical) != payload {
		return nil, ErrMaintenanceDiagnosticInvalid
	}
	return normalized, nil
}

func validDiagnosticIdentifier(value string, optional bool) bool {
	if value == "" {
		return optional
	}
	return len(value) <= maxMaintenanceDiagnosticPath && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "\\\x00\r\n") && !strings.Contains(value, "://") &&
		!path.IsAbs(value) && path.Clean(value) == value && value != "." && value != ".." && !strings.HasPrefix(value, "../")
}
