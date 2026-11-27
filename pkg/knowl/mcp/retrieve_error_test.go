package mcp

import (
	"errors"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
)

func TestCompactRetrieveSizeErrorMapsToReadLimit(t *testing.T) {
	if err := pageError(app.ErrPublicRetrieveResultTooLarge); !errors.Is(err, ErrReadLimit) {
		t.Fatalf("compact retrieve size error = %v, want read limit", err)
	}
}
