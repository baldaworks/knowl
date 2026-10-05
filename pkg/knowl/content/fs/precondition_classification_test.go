package fs_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
)

func TestPreconditionHasPermanentSafeClassification(t *testing.T) {
	cause := fmt.Errorf("private preimage evidence: %w", contentfs.ErrPrecondition)
	if !errors.Is(cause, contentfs.ErrPrecondition) {
		t.Fatal("precondition sentinel lost")
	}
	classification, ok := app.ClassifyExecutionFailure(cause)
	if !ok || classification.Class != "canonical_conflict" || classification.Reason != "precondition_failed" || classification.Retryable {
		t.Fatalf("precondition classification=%+v known=%v", classification, ok)
	}
	if _, ok := app.ClassifyExecutionFailure(contentfs.ErrPlanConflict); ok {
		t.Fatal("corrupt plan mislabeled as stale canonical preimage")
	}
}
