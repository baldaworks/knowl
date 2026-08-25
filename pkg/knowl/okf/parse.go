package okf

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const attestedComputationType = "Attested Computation"

var offsetTimestamp = regexp.MustCompile(`(?:Z|z|[+-][0-9]{2}:[0-9]{2})$`)

type fieldError struct {
	rule Rule
}

func (e *fieldError) Error() string {
	return string(e.rule)
}

// ParseConcept parses and validates one non-reserved OKF concept.
func ParseConcept(relative string, content []byte, limits Limits) (Document, error) {
	return parseConcept(relative, content, "", limits)
}

// ParseConceptWithDefaultType parses a concept and supplies defaultType only
// when the input omits type or declares it as an empty string. It is intended
// for producers normalizing generic Markdown into conformant OKF.
func ParseConceptWithDefaultType(relative string, content []byte, defaultType string, limits Limits) (Document, error) {
	if strings.TrimSpace(defaultType) == "" {
		return Document{}, violation(relative, RuleTypeMissing)
	}
	return parseConcept(relative, content, defaultType, limits)
}

func parseConcept(relative string, content []byte, defaultType string, limits Limits) (Document, error) {
	if !limits.valid() {
		return Document{}, violation(relative, RuleLimitsInvalid)
	}
	kind, err := ClassifyPath(relative)
	if err != nil {
		return Document{}, err
	}
	if kind != DocumentConcept {
		return Document{}, violation(relative, RulePathInvalid)
	}
	if len(content) > limits.MaxBytes {
		return Document{}, violation(relative, RuleSizeExceeded)
	}
	if !utf8.Valid(content) {
		return Document{}, violation(relative, RuleUTF8Invalid)
	}

	raw, body, present := splitFrontmatter(content)
	if !present {
		return Document{}, violation(relative, RuleFrontmatterMissing)
	}
	frontmatter, err := decodeYAMLMap(raw, limits)
	if err != nil {
		return Document{}, violation(relative, RuleFrontmatterMalformed)
	}
	if rawType, present := frontmatter["type"]; defaultType != "" {
		text, stringType := rawType.(string)
		if !present || (stringType && strings.TrimSpace(text) == "") {
			frontmatter["type"] = defaultType
		}
	}
	metadata, err := parseMetadata(frontmatter)
	if err != nil {
		var invalid *fieldError
		if errors.As(err, &invalid) {
			return Document{}, violation(relative, invalid.rule)
		}
		return Document{}, violation(relative, RuleMetadataInvalid)
	}

	return Document{Metadata: metadata, Body: body}, nil
}

// Body returns a validated concept's user-authored Markdown body.
func Body(relative string, content []byte, limits Limits) (string, error) {
	document, err := ParseConcept(relative, content, limits)
	if err != nil {
		return "", err
	}

	return document.Body, nil
}

func parseMetadata(fields map[string]any) (Metadata, error) {
	values := cloneMap(fields)
	metadata := Metadata{}
	var err error
	if metadata.Type, err = requiredString(values, "type", RuleTypeMissing); err != nil {
		return Metadata{}, err
	}
	if metadata.Title, err = optionalString(values, "title"); err != nil {
		return Metadata{}, err
	}
	if metadata.Description, err = optionalString(values, "description"); err != nil {
		return Metadata{}, err
	}
	if metadata.Resource, err = optionalString(values, "resource"); err != nil {
		return Metadata{}, err
	}
	if metadata.Tags, err = optionalStringList(values, "tags"); err != nil {
		return Metadata{}, err
	}
	if metadata.Sources, err = optionalSources(values); err != nil {
		return Metadata{}, err
	}
	if metadata.UsageWindow, err = optionalWindow(values, "usage_window"); err != nil {
		return Metadata{}, err
	}
	if metadata.Generated, err = optionalGeneration(values); err != nil {
		return Metadata{}, err
	}
	if metadata.Verified, err = optionalVerifications(values); err != nil {
		return Metadata{}, err
	}
	if metadata.Status, err = optionalStatus(values); err != nil {
		return Metadata{}, err
	}
	if metadata.StaleAfter, err = optionalTimestamp(values, "stale_after"); err != nil {
		return Metadata{}, err
	}
	if metadata.Runtime, err = optionalString(values, "runtime"); err != nil {
		return Metadata{}, err
	}
	if metadata.Parameters, err = optionalParameters(values); err != nil {
		return Metadata{}, err
	}
	if metadata.Computation, err = optionalString(values, "computation"); err != nil {
		return Metadata{}, err
	}
	if metadata.Executor, err = optionalExecutor(values); err != nil {
		return Metadata{}, err
	}
	if metadata.Attester, err = optionalAttester(values); err != nil {
		return Metadata{}, err
	}
	if metadata.Type == attestedComputationType && strings.TrimSpace(metadata.Runtime) == "" {
		return Metadata{}, &fieldError{rule: RuleMetadataInvalid}
	}
	metadata.Extensions = nilIfEmpty(values)

	return metadata, nil
}

func optionalSources(values map[string]any) ([]Source, error) {
	raw, present := take(values, "sources")
	if !present {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, &fieldError{rule: RuleMetadataInvalid}
	}
	result := make([]Source, 0, len(items))
	for _, item := range items {
		fields, ok := item.(map[string]any)
		if !ok {
			return nil, &fieldError{rule: RuleMetadataInvalid}
		}
		fields = cloneMap(fields)
		source := Source{}
		var err error
		if source.Resource, err = requiredString(fields, "resource", RuleMetadataInvalid); err != nil {
			return nil, err
		}
		if source.ID, err = optionalString(fields, "id"); err != nil {
			return nil, err
		}
		if source.Title, err = optionalString(fields, "title"); err != nil {
			return nil, err
		}
		if source.Author, err = optionalString(fields, "author"); err != nil {
			return nil, err
		}
		if source.UsageCount, err = optionalUint(fields, "usage_count"); err != nil {
			return nil, err
		}
		if source.LastModified, err = optionalTimestamp(fields, "last_modified"); err != nil {
			return nil, err
		}
		if source.UsageWindow, err = optionalWindow(fields, "usage_window"); err != nil {
			return nil, err
		}
		source.Extensions = nilIfEmpty(fields)
		result = append(result, source)
	}

	return result, nil
}

func optionalWindow(values map[string]any, key string) (*UsageWindow, error) {
	raw, present := take(values, key)
	if !present {
		return nil, nil
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil, &fieldError{rule: RuleMetadataInvalid}
	}
	fields = cloneMap(fields)
	from, err := requiredTimestamp(fields, "from")
	if err != nil {
		return nil, err
	}
	to, err := requiredTimestamp(fields, "to")
	if err != nil {
		return nil, err
	}

	return &UsageWindow{From: from, To: to, Extensions: nilIfEmpty(fields)}, nil
}

func optionalGeneration(values map[string]any) (*Generation, error) {
	raw, present := take(values, "generated")
	if !present {
		return nil, nil
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil, &fieldError{rule: RuleMetadataInvalid}
	}
	fields = cloneMap(fields)
	by, err := requiredString(fields, "by", RuleMetadataInvalid)
	if err != nil {
		return nil, err
	}
	at, err := optionalTimestamp(fields, "at")
	if err != nil {
		return nil, err
	}

	return &Generation{By: by, At: at, Extensions: nilIfEmpty(fields)}, nil
}

func optionalVerifications(values map[string]any) ([]Verification, error) {
	raw, present := take(values, "verified")
	if !present {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		if _, mapping := raw.(map[string]any); !mapping {
			return nil, &fieldError{rule: RuleMetadataInvalid}
		}
		items = []any{raw}
	}
	result := make([]Verification, 0, len(items))
	for _, item := range items {
		fields, ok := item.(map[string]any)
		if !ok {
			return nil, &fieldError{rule: RuleMetadataInvalid}
		}
		fields = cloneMap(fields)
		by, err := requiredString(fields, "by", RuleMetadataInvalid)
		if err != nil {
			return nil, err
		}
		at, err := requiredTimestamp(fields, "at")
		if err != nil {
			return nil, err
		}
		result = append(result, Verification{By: by, At: at, Extensions: nilIfEmpty(fields)})
	}

	return result, nil
}

func optionalStatus(values map[string]any) (Status, error) {
	raw, present := take(values, "status")
	if !present {
		return "", nil
	}
	status, ok := raw.(string)
	if !ok || (status != string(StatusDraft) && status != string(StatusStable) && status != string(StatusDeprecated)) {
		return "", &fieldError{rule: RuleMetadataInvalid}
	}

	return Status(status), nil
}

func optionalParameters(values map[string]any) ([]Parameter, error) {
	raw, present := take(values, "parameters")
	if !present {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, &fieldError{rule: RuleMetadataInvalid}
	}
	result := make([]Parameter, 0, len(items))
	for _, item := range items {
		fields, ok := item.(map[string]any)
		if !ok {
			return nil, &fieldError{rule: RuleMetadataInvalid}
		}
		fields = cloneMap(fields)
		name, err := requiredString(fields, "name", RuleMetadataInvalid)
		if err != nil {
			return nil, err
		}
		typeName, err := requiredString(fields, "type", RuleMetadataInvalid)
		if err != nil {
			return nil, err
		}
		required, err := requiredBool(fields, "required")
		if err != nil {
			return nil, err
		}
		result = append(result, Parameter{Name: name, Type: typeName, Required: required, Extensions: nilIfEmpty(fields)})
	}

	return result, nil
}

func optionalExecutor(values map[string]any) (*Executor, error) {
	raw, present := take(values, "executor")
	if !present {
		return nil, nil
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil, &fieldError{rule: RuleMetadataInvalid}
	}
	fields = cloneMap(fields)
	resource, err := requiredString(fields, "resource", RuleMetadataInvalid)
	if err != nil {
		return nil, err
	}
	receipt, err := optionalStringList(fields, "receipt")
	if err != nil {
		return nil, err
	}

	return &Executor{Resource: resource, Receipt: receipt, Extensions: nilIfEmpty(fields)}, nil
}

func optionalAttester(values map[string]any) (*Attester, error) {
	raw, present := take(values, "attester")
	if !present {
		return nil, nil
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil, &fieldError{rule: RuleMetadataInvalid}
	}
	fields = cloneMap(fields)
	resource, err := requiredString(fields, "resource", RuleMetadataInvalid)
	if err != nil {
		return nil, err
	}

	return &Attester{Resource: resource, Extensions: nilIfEmpty(fields)}, nil
}

func requiredString(values map[string]any, key string, rule Rule) (string, error) {
	value, present := take(values, key)
	text, ok := value.(string)
	if !present || !ok || strings.TrimSpace(text) == "" {
		return "", &fieldError{rule: rule}
	}

	return text, nil
}

func optionalString(values map[string]any, key string) (string, error) {
	value, present := take(values, key)
	if !present {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", &fieldError{rule: RuleMetadataInvalid}
	}

	return text, nil
}

func optionalStringList(values map[string]any, key string) ([]string, error) {
	value, present := take(values, key)
	if !present {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, &fieldError{rule: RuleMetadataInvalid}
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, &fieldError{rule: RuleMetadataInvalid}
		}
		result = append(result, text)
	}

	return result, nil
}

func optionalUint(values map[string]any, key string) (*uint64, error) {
	value, present := take(values, key)
	if !present {
		return nil, nil
	}
	var result uint64
	switch typed := value.(type) {
	case int64:
		if typed < 0 {
			return nil, &fieldError{rule: RuleMetadataInvalid}
		}
		result = uint64(typed)
	case uint64:
		result = typed
	default:
		return nil, &fieldError{rule: RuleMetadataInvalid}
	}

	return &result, nil
}

func requiredBool(values map[string]any, key string) (bool, error) {
	value, present := take(values, key)
	result, ok := value.(bool)
	if !present || !ok {
		return false, &fieldError{rule: RuleMetadataInvalid}
	}

	return result, nil
}

func optionalTimestamp(values map[string]any, key string) (*time.Time, error) {
	value, present := take(values, key)
	if !present {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, &fieldError{rule: RuleTimestampInvalid}
	}
	parsed, err := parseTimestamp(text)
	if err != nil {
		return nil, err
	}

	return &parsed, nil
}

func requiredTimestamp(values map[string]any, key string) (time.Time, error) {
	value, present := take(values, key)
	text, ok := value.(string)
	if !present || !ok {
		return time.Time{}, &fieldError{rule: RuleTimestampInvalid}
	}

	return parseTimestamp(text)
}

func parseTimestamp(value string) (time.Time, error) {
	if !offsetTimestamp.MatchString(value) {
		return time.Time{}, &fieldError{rule: RuleTimestampInvalid}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, &fieldError{rule: RuleTimestampInvalid}
	}

	return parsed, nil
}

func take(values map[string]any, key string) (any, bool) {
	value, present := values[key]
	delete(values, key)

	return value, present
}

func cloneMap(values map[string]any) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}

	return result
}

func nilIfEmpty(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}

	return values
}
