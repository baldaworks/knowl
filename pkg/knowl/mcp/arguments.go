package mcp

import (
	"fmt"
	"strings"
)

func argumentString(arguments map[string]any, names ...string) (string, error) {
	for _, name := range names {
		value, present := arguments[name]
		if !present {
			continue
		}
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("argument %q must be a non-empty string: %w", name, ErrInvalidArguments)
		}
		return strings.TrimSpace(text), nil
	}
	return "", fmt.Errorf("one of %v is required: %w", names, ErrInvalidArguments)
}

func optionalString(arguments map[string]any, name string) (string, error) {
	value, present := arguments[name]
	if !present {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string: %w", name, ErrInvalidArguments)
	}
	return strings.TrimSpace(text), nil
}
