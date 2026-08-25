package okf

import (
	"errors"
	"fmt"
)

// Rule identifies a stable OKF validation rule.
type Rule string

const (
	// RuleLimitsInvalid rejects an invalid parser or renderer limit set.
	RuleLimitsInvalid Rule = "okf.limits.invalid"
	// RulePathInvalid rejects unsafe or non-canonical bundle-relative paths.
	RulePathInvalid Rule = "okf.path.invalid"
	// RuleSizeExceeded rejects a document beyond the configured byte limit.
	RuleSizeExceeded Rule = "okf.size.exceeded"
	// RuleUTF8Invalid rejects non-UTF-8 content.
	RuleUTF8Invalid Rule = "okf.utf8.invalid"
	// RuleFrontmatterMissing rejects a concept without leading frontmatter.
	RuleFrontmatterMissing Rule = "okf.frontmatter.missing"
	// RuleFrontmatterMalformed rejects malformed or unsafe YAML frontmatter.
	RuleFrontmatterMalformed Rule = "okf.frontmatter.malformed"
	// RuleTypeMissing rejects an absent or empty concept type.
	RuleTypeMissing Rule = "okf.type.missing"
	// RuleMetadataInvalid rejects malformed standard metadata.
	RuleMetadataInvalid Rule = "okf.metadata.invalid"
	// RuleTimestampInvalid rejects a timestamp without an explicit UTC offset.
	RuleTimestampInvalid Rule = "okf.timestamp.invalid"
	// RuleIndexInvalid rejects a malformed reserved index document.
	RuleIndexInvalid Rule = "okf.index.invalid"
	// RuleLogInvalid rejects a malformed reserved log document.
	RuleLogInvalid Rule = "okf.log.invalid"
)

// ErrInvalid is matched by every OKF validation violation.
var ErrInvalid = errors.New("invalid OKF document")

// Violation is a redacted validation error containing no document content or
// host filesystem path.
type Violation struct {
	Path string
	Rule Rule
}

// Error returns the stable relative path and rule code.
func (v *Violation) Error() string {
	return fmt.Sprintf("invalid OKF document %q: %s", v.Path, v.Rule)
}

// Unwrap makes all violations match ErrInvalid.
func (v *Violation) Unwrap() error {
	return ErrInvalid
}

func violation(relative string, rule Rule) error {
	return &Violation{Path: safeErrorPath(relative), Rule: rule}
}
