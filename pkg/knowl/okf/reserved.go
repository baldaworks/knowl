package okf

import (
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// ParseRootIndex parses the bundle-root index.md and observes its declared
// version. Missing and unknown versions are accepted for best-effort reading.
func ParseRootIndex(content []byte, limits Limits) (Index, error) {
	return ValidateIndex(indexFilename, content, limits)
}

// ValidateIndex validates one reserved index.md at any bundle depth.
func ValidateIndex(relative string, content []byte, limits Limits) (Index, error) {
	if !limits.valid() {
		return Index{}, violation(relative, RuleLimitsInvalid)
	}
	kind, err := ClassifyPath(relative)
	if err != nil {
		return Index{}, err
	}
	if kind != DocumentIndex {
		return Index{}, violation(relative, RulePathInvalid)
	}
	if len(content) > limits.MaxBytes {
		return Index{}, violation(relative, RuleSizeExceeded)
	}
	if !utf8.Valid(content) {
		return Index{}, violation(relative, RuleUTF8Invalid)
	}

	raw, body, present := splitFrontmatter(content)
	index := Index{Body: string(content)}
	if present {
		if relative != indexFilename {
			return Index{}, violation(relative, RuleIndexInvalid)
		}
		fields, err := decodeYAMLMap(raw, limits)
		if err != nil || len(fields) != 1 {
			return Index{}, violation(relative, RuleIndexInvalid)
		}
		version, ok := fields["okf_version"].(string)
		if !ok || strings.TrimSpace(version) == "" {
			return Index{}, violation(relative, RuleIndexInvalid)
		}
		index.ObservedVersion = version
		index.Body = body
	}
	if !validIndexBody(index.Body) {
		return Index{}, violation(relative, RuleIndexInvalid)
	}

	return index, nil
}

// EffectiveVersion returns v0.2 when a root index does not declare a version.
func (index Index) EffectiveVersion() string {
	if index.ObservedVersion == "" {
		return Version
	}

	return index.ObservedVersion
}

// RenderIndex deterministically renders and validates a reserved index.
func RenderIndex(relative string, index Index, limits Limits) ([]byte, error) {
	if relative != indexFilename && index.ObservedVersion != "" {
		return nil, violation(relative, RuleIndexInvalid)
	}
	var builder strings.Builder
	if relative == indexFilename && index.ObservedVersion != "" {
		raw, err := yaml.Marshal(map[string]any{"okf_version": index.ObservedVersion})
		if err != nil {
			return nil, violation(relative, RuleIndexInvalid)
		}
		builder.WriteString("---\n")
		builder.Write(raw)
		builder.WriteString("---\n")
	}
	builder.WriteString(index.Body)
	rendered := []byte(builder.String())
	if _, err := ValidateIndex(relative, rendered, limits); err != nil {
		return nil, err
	}

	return rendered, nil
}

// ValidateLog parses and validates a newest-first, ISO-date-grouped log.md.
func ValidateLog(relative string, content []byte, limits Limits) (Log, error) {
	if !limits.valid() {
		return Log{}, violation(relative, RuleLimitsInvalid)
	}
	kind, err := ClassifyPath(relative)
	if err != nil {
		return Log{}, err
	}
	if kind != DocumentLog {
		return Log{}, violation(relative, RulePathInvalid)
	}
	if len(content) > limits.MaxBytes {
		return Log{}, violation(relative, RuleSizeExceeded)
	}
	if !utf8.Valid(content) {
		return Log{}, violation(relative, RuleUTF8Invalid)
	}
	if _, _, present := splitFrontmatter(content); present {
		return Log{}, violation(relative, RuleLogInvalid)
	}

	parsed, ok := parseLog(string(content))
	if !ok {
		return Log{}, violation(relative, RuleLogInvalid)
	}

	return parsed, nil
}

// RenderLog deterministically renders and validates a reserved log.
func RenderLog(relative string, logDocument Log, limits Limits) ([]byte, error) {
	var builder strings.Builder
	builder.WriteString("# ")
	builder.WriteString(logDocument.Title)
	builder.WriteString("\n")
	for _, group := range logDocument.Groups {
		builder.WriteString("\n## ")
		builder.WriteString(group.Date.Format(time.DateOnly))
		builder.WriteString("\n")
		for _, entry := range group.Entries {
			builder.WriteString("* ")
			builder.WriteString(entry)
			builder.WriteString("\n")
		}
	}
	rendered := []byte(builder.String())
	if _, err := ValidateLog(relative, rendered, limits); err != nil {
		return nil, err
	}

	return rendered, nil
}

func validIndexBody(body string) bool {
	foundHeading := false
	foundEntry := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "# ") && strings.TrimSpace(strings.TrimPrefix(trimmed, "# ")) != "" {
			foundHeading = true
			foundEntry = false
			continue
		}
		if !foundHeading || trimmed == "#" || strings.HasPrefix(trimmed, "##") {
			return false
		}
		if strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "- ") {
			if !validIndexEntry(strings.TrimSpace(trimmed[2:])) {
				return false
			}
			foundEntry = true
			continue
		}
		if !foundEntry || (line[0] != ' ' && line[0] != '\t') {
			return false
		}
	}

	return foundHeading
}

func validIndexEntry(entry string) bool {
	if !strings.HasPrefix(entry, "[") {
		return false
	}
	separator := strings.Index(entry, "](")
	if separator <= 1 {
		return false
	}
	end := strings.Index(entry[separator+2:], ")")
	return end > 0
}

func parseLog(content string) (Log, bool) {
	result := Log{}
	var current *LogGroup
	var previous time.Time
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if result.Title == "" {
			if !strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ") {
				return Log{}, false
			}
			result.Title = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			if result.Title == "" {
				return Log{}, false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "## ") {
			if current != nil && len(current.Entries) == 0 {
				return Log{}, false
			}
			date, err := time.Parse(time.DateOnly, strings.TrimSpace(strings.TrimPrefix(trimmed, "## ")))
			if err != nil || (!previous.IsZero() && !date.Before(previous)) {
				return Log{}, false
			}
			result.Groups = append(result.Groups, LogGroup{Date: date})
			current = &result.Groups[len(result.Groups)-1]
			previous = date
			continue
		}
		if current == nil || (!strings.HasPrefix(trimmed, "* ") && !strings.HasPrefix(trimmed, "- ")) {
			return Log{}, false
		}
		entry := strings.TrimSpace(trimmed[2:])
		if entry == "" {
			return Log{}, false
		}
		current.Entries = append(current.Entries, entry)
	}
	if result.Title == "" || (current != nil && len(current.Entries) == 0) {
		return Log{}, false
	}

	return result, true
}
