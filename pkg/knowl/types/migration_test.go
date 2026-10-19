package knowl

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestMigrationErrorPreservesCauseAndRedactsFormatting(t *testing.T) {
	cause := fmt.Errorf("secret-password: %w", context.Canceled)
	failure := &MigrationError{Stage: MigrationStageApply, Stream: "base", Version: 26, Cause: cause}
	if !errors.Is(failure, context.Canceled) {
		t.Fatal("cause lost")
	}
	if got := fmt.Sprint(failure); got != "database migration failed (stage=apply stream=base version=26)" {
		t.Fatalf("unsafe error rendering: %q", got)
	}
	unsafeMetadata := &MigrationError{Stage: "secret-stage", Stream: "secret-stream", SQLState: "secret-state", Cause: cause}
	if got := fmt.Sprint(unsafeMetadata); got != "database migration failed (stage=unknown stream=unknown version=0)" {
		t.Fatalf("unsafe metadata rendering: %q", got)
	}
	classified := &MigrationError{Stage: MigrationStageApply, Stream: "pgvector", Version: 1, SQLState: "42501", Cause: cause}
	if got := fmt.Sprint(classified); got != "database migration failed (stage=apply stream=pgvector version=1 sqlstate=42501)" {
		t.Fatalf("unsafe SQLSTATE rendering: %q", got)
	}
}
