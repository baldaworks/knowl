package mcp

import (
	"fmt"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/types"
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

func optionalSourceIDs(arguments map[string]any, name string) ([]knowl.SourceID, error) {
	value, present := arguments[name]
	if !present {
		return nil, nil
	}
	var values []string
	switch typed := value.(type) {
	case []string:
		values = typed
	case []any:
		values = make([]string, len(typed))
		for index, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("argument %q must be an array of strings: %w", name, ErrInvalidArguments)
			}
			values[index] = text
		}
	default:
		return nil, fmt.Errorf("argument %q must be an array of strings: %w", name, ErrInvalidArguments)
	}
	sources := make([]knowl.SourceID, len(values))
	for index, value := range values {
		sources[index] = knowl.SourceID(value)
	}
	return sources, nil
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
