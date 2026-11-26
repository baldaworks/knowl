package app

import (
	"fmt"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// HierarchyGraphError carries a bounded rule identifier and validated wiki paths.
// It never exposes titles, query text, provider output or upstream error bodies.
type HierarchyGraphError struct {
	Diagnostic knowl.MaintenanceDiagnostic
	cause      error
}

func (err *HierarchyGraphError) Error() string {
	return fmt.Sprintf("%s at %s: %v", err.Diagnostic.Code, err.Diagnostic.Path, err.cause)
}
func (err *HierarchyGraphError) Unwrap() error { return err.cause }
func (err *HierarchyGraphError) MaintenanceDiagnostics() []knowl.MaintenanceDiagnostic {
	return []knowl.MaintenanceDiagnostic{err.Diagnostic}
}

func hierarchyGraphError(code, path, target string, cause error) error {
	return &HierarchyGraphError{Diagnostic: knowl.MaintenanceDiagnostic{Code: code, Path: path, Target: target}, cause: cause}
}
