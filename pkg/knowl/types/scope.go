package knowl

import (
	"errors"
	"fmt"
	"strings"
)

const maxScopeNameLength = 63

var (
	// ErrScopeNameInvalid identifies a caller-visible scope name outside the
	// conservative lowercase slug contract.
	ErrScopeNameInvalid = errors.New("invalid scope name")
	// ErrScopeUnknown identifies a valid public name that is not configured.
	ErrScopeUnknown = errors.New("unknown scope")
	// ErrScopeDisabled identifies a configured scope that cannot accept work.
	ErrScopeDisabled = errors.New("disabled scope")
)

// ScopeName is the validated caller-visible name of a configured scope.
type ScopeName string

// ParseScopeName validates and returns one public scope name.
func ParseScopeName(value string) (ScopeName, error) {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxScopeNameLength || !scopeNameStart(value[0]) {
		return "", fmt.Errorf("%w: %q", ErrScopeNameInvalid, value)
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if !scopeNameStart(character) && character != '-' {
			return "", fmt.Errorf("%w: %q", ErrScopeNameInvalid, value)
		}
	}
	return ScopeName(value), nil
}

func scopeNameStart(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
}
