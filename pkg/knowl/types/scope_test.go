package knowl

import (
	"errors"
	"strings"
	"testing"
)

func TestParseScopeName(t *testing.T) {
	t.Parallel()
	valid := []string{"0", "default", "infrastructure-2", "trailing-", strings.Repeat("a", 63)}
	for _, value := range valid {
		t.Run("valid_"+value, func(t *testing.T) {
			t.Parallel()
			name, err := ParseScopeName(value)
			if err != nil || string(name) != value {
				t.Fatalf("ParseScopeName(%q) = %q, %v", value, name, err)
			}
		})
	}
	invalid := []string{"", "-", "Default", "with_underscore", "two words", " default", strings.Repeat("a", 64), "é"}
	for _, value := range invalid {
		t.Run("invalid_"+value, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseScopeName(value); !errors.Is(err, ErrScopeNameInvalid) {
				t.Fatalf("ParseScopeName(%q) error = %v, want ErrScopeNameInvalid", value, err)
			}
		})
	}
}
