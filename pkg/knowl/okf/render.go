package okf

import (
	"math"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const resourceField = "resource"

// RenderConcept renders a concept deterministically and validates the result.
func RenderConcept(relative string, document Document, limits Limits) ([]byte, error) {
	if !limits.valid() {
		return nil, violation(relative, RuleLimitsInvalid)
	}
	kind, err := ClassifyPath(relative)
	if err != nil {
		return nil, err
	}
	if kind != DocumentConcept {
		return nil, violation(relative, RulePathInvalid)
	}

	frontmatter, err := metadataMap(document.Metadata)
	if err != nil {
		return nil, violation(relative, RuleMetadataInvalid)
	}
	budget := limits.MaxNodes
	if err := validateTransportValue(frontmatter, 1, limits.MaxDepth, &budget); err != nil {
		return nil, violation(relative, RuleFrontmatterMalformed)
	}
	raw, err := yaml.Marshal(frontmatter)
	if err != nil {
		return nil, violation(relative, RuleFrontmatterMalformed)
	}
	rendered := []byte("---\n" + string(raw) + "---\n" + document.Body)
	if len(rendered) > limits.MaxBytes {
		return nil, violation(relative, RuleSizeExceeded)
	}
	if _, err := ParseConcept(relative, rendered, limits); err != nil {
		return nil, err
	}

	return rendered, nil
}

func metadataMap(metadata Metadata) (map[string]any, error) {
	result, err := object(metadata.Extensions, map[string]any{"type": metadata.Type},
		"type", "title", "description", "resource", "tags", "sources", "usage_window",
		"generated", "verified", "status", "stale_after", "runtime", "parameters",
		"computation", "executor", "attester")
	if err != nil {
		return nil, err
	}
	putString(result, "title", metadata.Title)
	putString(result, "description", metadata.Description)
	putString(result, "resource", metadata.Resource)
	if metadata.Tags != nil {
		result["tags"] = metadata.Tags
	}
	if metadata.Sources != nil {
		items := make([]any, 0, len(metadata.Sources))
		for _, source := range metadata.Sources {
			item, err := sourceMap(source)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		result["sources"] = items
	}
	if metadata.UsageWindow != nil {
		window, err := windowMap(*metadata.UsageWindow)
		if err != nil {
			return nil, err
		}
		result["usage_window"] = window
	}
	if metadata.Generated != nil {
		generated, err := object(metadata.Generated.Extensions, map[string]any{"by": metadata.Generated.By}, "by", "at")
		if err != nil {
			return nil, err
		}
		if metadata.Generated.At != nil {
			generated["at"] = formatTimestamp(*metadata.Generated.At)
		}
		result["generated"] = generated
	}
	if metadata.Verified != nil {
		items := make([]any, 0, len(metadata.Verified))
		for _, verification := range metadata.Verified {
			item, err := object(verification.Extensions, map[string]any{
				"by": verification.By,
				"at": formatTimestamp(verification.At),
			}, "by", "at")
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		result["verified"] = items
	}
	if metadata.Status != "" {
		result["status"] = string(metadata.Status)
	}
	if metadata.StaleAfter != nil {
		result["stale_after"] = formatTimestamp(*metadata.StaleAfter)
	}
	putString(result, "runtime", metadata.Runtime)
	if metadata.Parameters != nil {
		items := make([]any, 0, len(metadata.Parameters))
		for _, parameter := range metadata.Parameters {
			item, err := object(parameter.Extensions, map[string]any{
				"name":     parameter.Name,
				"type":     parameter.Type,
				"required": parameter.Required,
			}, "name", "type", "required")
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		result["parameters"] = items
	}
	putString(result, "computation", metadata.Computation)
	if metadata.Executor != nil {
		executor, err := object(metadata.Executor.Extensions, map[string]any{resourceField: metadata.Executor.Resource}, resourceField, "receipt")
		if err != nil {
			return nil, err
		}
		if metadata.Executor.Receipt != nil {
			executor["receipt"] = metadata.Executor.Receipt
		}
		result["executor"] = executor
	}
	if metadata.Attester != nil {
		attester, err := object(metadata.Attester.Extensions, map[string]any{resourceField: metadata.Attester.Resource}, resourceField)
		if err != nil {
			return nil, err
		}
		result["attester"] = attester
	}

	return result, nil
}

func sourceMap(source Source) (map[string]any, error) {
	result, err := object(source.Extensions, map[string]any{resourceField: source.Resource},
		"id", resourceField, "title", "author", "usage_count", "last_modified", "usage_window")
	if err != nil {
		return nil, err
	}
	putString(result, "id", source.ID)
	putString(result, "title", source.Title)
	putString(result, "author", source.Author)
	if source.UsageCount != nil {
		result["usage_count"] = *source.UsageCount
	}
	if source.LastModified != nil {
		result["last_modified"] = formatTimestamp(*source.LastModified)
	}
	if source.UsageWindow != nil {
		window, err := windowMap(*source.UsageWindow)
		if err != nil {
			return nil, err
		}
		result["usage_window"] = window
	}

	return result, nil
}

func windowMap(window UsageWindow) (map[string]any, error) {
	return object(window.Extensions, map[string]any{
		"from": formatTimestamp(window.From),
		"to":   formatTimestamp(window.To),
	}, "from", "to")
}

func object(extensions, known map[string]any, reserved ...string) (map[string]any, error) {
	result := make(map[string]any, len(extensions)+len(known))
	for key, value := range extensions {
		if key == "" || contains(reserved, key) {
			return nil, &fieldError{rule: RuleMetadataInvalid}
		}
		result[key] = value
	}
	for key, value := range known {
		result[key] = value
	}

	return result, nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}

	return false
}

func putString(values map[string]any, key, value string) {
	if value != "" {
		values[key] = value
	}
}

func formatTimestamp(value time.Time) string {
	return value.Format(time.RFC3339Nano)
}

func validateTransportValue(value any, depth, maxDepth int, budget *int) error {
	if depth > maxDepth || *budget <= 0 {
		return &fieldError{rule: RuleMetadataInvalid}
	}
	*budget--

	switch typed := value.(type) {
	case nil, bool, string, int64, uint64:
		return nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return &fieldError{rule: RuleMetadataInvalid}
		}
		return nil
	case []string:
		for _, item := range typed {
			if err := validateTransportValue(item, depth+1, maxDepth, budget); err != nil {
				return err
			}
		}
		return nil
	case []any:
		for _, item := range typed {
			if err := validateTransportValue(item, depth+1, maxDepth, budget); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for key, item := range typed {
			if strings.TrimSpace(key) == "" {
				return &fieldError{rule: RuleMetadataInvalid}
			}
			if err := validateTransportValue(item, depth+1, maxDepth, budget); err != nil {
				return err
			}
		}
		return nil
	default:
		return &fieldError{rule: RuleMetadataInvalid}
	}
}
