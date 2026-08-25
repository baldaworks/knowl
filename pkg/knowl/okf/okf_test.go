package okf_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
)

const referenceType = "Reference"

func TestClassifyPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want okf.DocumentKind
	}{
		{path: "Глоссарий-проекта.md", want: okf.DocumentConcept},
		{path: "deep/ещё/index.md", want: okf.DocumentIndex},
		{path: "deep/log.md", want: okf.DocumentLog},
		{path: strings.Repeat("a", 1500) + ".md", want: okf.DocumentConcept},
		{path: "references/schema.json", want: okf.DocumentAsset},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			t.Parallel()
			got, err := okf.ClassifyPath(test.path)
			if err != nil || got != test.want {
				t.Fatalf("ClassifyPath(%q) = %q, %v; want %q", test.path, got, err, test.want)
			}
		})
	}

	for _, invalid := range []string{"", "/absolute.md", "../escape.md", "a/../b.md", "back\\slash.md", " bad.md", "a\tb.md"} {
		_, err := okf.ClassifyPath(invalid)
		assertViolation(t, err, "<invalid-path>", okf.RulePathInvalid)
	}
}

func TestParseRenderConceptFullMetadata(t *testing.T) {
	t.Parallel()

	content := []byte(`---
type: Attested Computation
title: Revenue
description: Recognized revenue.
resource: https://example.test/revenue
tags: [finance, revenue]
sources:
  - id: policy
    resource: https://example.test/policy
    title: Policy
    author: team:finance
    usage_count: 5000
    last_modified: 2026-05-30T00:00:00+06:00
    usage_window: {from: 2026-05-01T00:00:00Z, to: 2026-05-31T00:00:00Z, precision: day}
    confidence: high
usage_window: {from: 2026-06-01T00:00:00Z, to: 2026-06-30T00:00:00Z}
generated: {by: agent/v1, at: 2026-06-20T22:53:05Z, run: nightly}
verified: {by: human:alexey, at: 2026-06-25T09:00:00Z, ticket: OK-1}
status: deprecated
stale_after: 2026-09-23T00:00:00Z
runtime: bigquery
parameters:
  - {name: year, type: integer, required: true, minimum: 2000}
computation: references/revenue.sql
executor: {resource: references/run.md, receipt: [job_id, result], mode: batch}
attester: {resource: references/attest.py, deterministic: true}
producer_extension:
  nested: [one, {enabled: true, count: 3}]
---
# Computation

Use the sanctioned query.
`)

	document, err := okf.ParseConcept("computations/выручка.md", content, okf.DefaultLimits())
	if err != nil {
		t.Fatalf("ParseConcept() error = %v", err)
	}
	metadata := document.Metadata
	if metadata.Type != "Attested Computation" || metadata.Title != "Revenue" || metadata.Runtime != "bigquery" {
		t.Fatalf("unexpected core metadata: %#v", metadata)
	}
	if len(metadata.Sources) != 1 || metadata.Sources[0].UsageCount == nil || *metadata.Sources[0].UsageCount != 5000 {
		t.Fatalf("unexpected sources: %#v", metadata.Sources)
	}
	if len(metadata.Verified) != 1 || okf.DeriveTrustTier(metadata) != okf.TrustHumanReviewed {
		t.Fatalf("bare verified mapping was not normalized: %#v", metadata.Verified)
	}
	if metadata.EffectiveStatus() != okf.StatusDeprecated || metadata.StaleAfter == nil {
		t.Fatalf("unexpected lifecycle metadata: %#v", metadata)
	}
	if okf.IsStale(metadata, metadata.StaleAfter.Add(-time.Nanosecond)) || !okf.IsStale(metadata, *metadata.StaleAfter) {
		t.Fatal("staleness boundary is not inclusive")
	}
	derived := okf.WithDerivedSemantics(metadata, *metadata.StaleAfter)
	if derived.TrustTier != okf.TrustHumanReviewed || derived.ResolvedStatus != okf.StatusDeprecated || !derived.Stale || metadata.TrustTier != "" {
		t.Fatalf("derived metadata mutated input or lost semantics: derived=%#v input=%#v", derived, metadata)
	}
	if !strings.HasPrefix(document.Body, "# Computation") {
		t.Fatalf("Body = %q", document.Body)
	}
	producer, ok := metadata.Extensions["producer_extension"].(map[string]any)
	if !ok || producer["nested"] == nil {
		t.Fatalf("unknown nested extension was not retained: %#v", metadata.Extensions)
	}

	rendered, err := okf.RenderConcept("computations/выручка.md", document, okf.DefaultLimits())
	if err != nil {
		t.Fatalf("RenderConcept() error = %v", err)
	}
	renderedAgain, err := okf.RenderConcept("computations/выручка.md", document, okf.DefaultLimits())
	if err != nil {
		t.Fatalf("RenderConcept() second error = %v", err)
	}
	if string(rendered) != string(renderedAgain) {
		t.Fatal("RenderConcept() is not deterministic")
	}
	roundTrip, err := okf.ParseConcept("computations/выручка.md", rendered, okf.DefaultLimits())
	if err != nil {
		t.Fatalf("ParseConcept(rendered) error = %v", err)
	}
	if roundTrip.Body != document.Body || roundTrip.Metadata.Type != metadata.Type ||
		roundTrip.Metadata.Extensions["producer_extension"] == nil || len(roundTrip.Metadata.Verified) != 1 {
		t.Fatalf("semantic round trip failed: %#v", roundTrip)
	}
}

func TestConceptDefaultsAndTrustTiers(t *testing.T) {
	t.Parallel()

	minimal, err := okf.ParseConcept("unknown.md", []byte("---\ntype: Future Type\nextra: null\n---\nbody"), okf.DefaultLimits())
	if err != nil {
		t.Fatalf("ParseConcept(minimal) error = %v", err)
	}
	if minimal.Metadata.EffectiveStatus() != okf.StatusStable || okf.DeriveTrustTier(minimal.Metadata) != okf.TrustUnverified {
		t.Fatalf("unexpected defaults: %#v", minimal.Metadata)
	}

	machine, err := okf.ParseConcept("machine.md", []byte(`---
type: Reference
verified:
  - {by: process:nightly, at: 2026-06-25T09:00:00Z}
  - {by: agent/v1, at: 2026-06-26T09:00:00+06:00}
---
`), okf.DefaultLimits())
	if err != nil {
		t.Fatalf("ParseConcept(machine) error = %v", err)
	}
	if okf.DeriveTrustTier(machine.Metadata) != okf.TrustMachineConfirmed {
		t.Fatalf("trust tier = %q", okf.DeriveTrustTier(machine.Metadata))
	}
}

func TestBoundedAliasIsNormalized(t *testing.T) {
	t.Parallel()

	document, err := okf.ParseConcept("alias.md", []byte("---\ntype: &kind Reference\ntitle: *kind\n---\n"), okf.DefaultLimits())
	if err != nil {
		t.Fatalf("ParseConcept(alias) error = %v", err)
	}
	if document.Metadata.Type != referenceType || document.Metadata.Title != referenceType {
		t.Fatalf("alias was not normalized: %#v", document.Metadata)
	}
}

func TestParseConceptWithDefaultType(t *testing.T) {
	t.Parallel()

	document, err := okf.ParseConceptWithDefaultType("generic.md", []byte("---\ntitle: Generic\ntags: [one]\n---\nbody"), referenceType, okf.DefaultLimits())
	if err != nil {
		t.Fatalf("ParseConceptWithDefaultType() error = %v", err)
	}
	if document.Metadata.Type != referenceType || document.Metadata.Title != "Generic" || len(document.Metadata.Tags) != 1 {
		t.Fatalf("metadata = %#v", document.Metadata)
	}
	if _, err := okf.ParseConcept("generic.md", []byte("---\ntitle: Generic\n---\nbody"), okf.DefaultLimits()); err == nil {
		t.Fatal("ParseConcept() unexpectedly supplied a default type")
	}
}

func TestParseConceptViolationsAreBoundedAndRedacted(t *testing.T) {
	t.Parallel()

	limits := okf.DefaultLimits()
	tests := []struct {
		name    string
		content []byte
		limits  okf.Limits
		rule    okf.Rule
	}{
		{name: "missing frontmatter", content: []byte("secret body"), limits: limits, rule: okf.RuleFrontmatterMissing},
		{name: "malformed", content: []byte("---\n[type\n---\nsecret"), limits: limits, rule: okf.RuleFrontmatterMalformed},
		{name: "missing type", content: []byte("---\ntitle: secret\n---\n"), limits: limits, rule: okf.RuleTypeMissing},
		{name: "empty type", content: []byte("---\ntype: '  '\n---\n"), limits: limits, rule: okf.RuleTypeMissing},
		{name: "invalid timestamp", content: []byte("---\ntype: Reference\nstale_after: 2026-01-01T00:00:00\n---\n"), limits: limits, rule: okf.RuleTimestampInvalid},
		{name: "invalid utf8", content: []byte{'-', '-', '-', '\n', 't', 'y', 'p', 'e', ':', ' ', 0xff}, limits: limits, rule: okf.RuleUTF8Invalid},
		{name: "alias bound", content: []byte("---\ntype: &kind Reference\ntitle: *kind\n---\n"), limits: withAliases(limits, 0), rule: okf.RuleFrontmatterMalformed},
		{name: "node bound", content: []byte("---\ntype: Reference\ntitle: T\n---\n"), limits: withNodes(limits, 4), rule: okf.RuleFrontmatterMalformed},
		{name: "depth bound", content: []byte("---\ntype: Reference\next: {a: {b: value}}\n---\n"), limits: withDepth(limits, 3), rule: okf.RuleFrontmatterMalformed},
		{name: "duplicate key", content: []byte("---\ntype: Reference\ntype: Other\n---\n"), limits: limits, rule: okf.RuleFrontmatterMalformed},
		{name: "oversized", content: []byte("---\ntype: Reference\n---\n"), limits: withBytes(limits, 8), rule: okf.RuleSizeExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := okf.ParseConcept("safe/документ.md", test.content, test.limits)
			assertViolation(t, err, "safe/документ.md", test.rule)
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), string(test.content)) {
				t.Fatalf("error leaks content: %v", err)
			}
		})
	}
}

func TestBodyAndRenderRejectUnsafeValues(t *testing.T) {
	t.Parallel()

	body, err := okf.Body("page.md", []byte("---\ntype: Reference\n---\nhello"), okf.DefaultLimits())
	if err != nil || body != "hello" {
		t.Fatalf("Body() = %q, %v", body, err)
	}

	_, err = okf.RenderConcept("page.md", okf.Document{Metadata: okf.Metadata{
		Type:       "Reference",
		Extensions: map[string]any{"title": "collision"},
	}}, okf.DefaultLimits())
	assertViolation(t, err, "page.md", okf.RuleMetadataInvalid)

	_, err = okf.RenderConcept("page.md", okf.Document{Metadata: okf.Metadata{
		Type:       "Reference",
		Extensions: map[string]any{"unsupported": make(chan int)},
	}}, okf.DefaultLimits())
	assertViolation(t, err, "page.md", okf.RuleFrontmatterMalformed)

	invalidLimits := okf.DefaultLimits()
	invalidLimits.MaxNodes = 0
	_, err = okf.ParseConcept("page.md", []byte("---\ntype: Reference\n---\n"), invalidLimits)
	assertViolation(t, err, "page.md", okf.RuleLimitsInvalid)
}

func withBytes(limits okf.Limits, maximum int) okf.Limits {
	limits.MaxBytes = maximum
	return limits
}

func withNodes(limits okf.Limits, maximum int) okf.Limits {
	limits.MaxNodes = maximum
	return limits
}

func withAliases(limits okf.Limits, maximum int) okf.Limits {
	limits.MaxAliases = maximum
	return limits
}

func withDepth(limits okf.Limits, maximum int) okf.Limits {
	limits.MaxDepth = maximum
	return limits
}

func assertViolation(t *testing.T, err error, path string, rule okf.Rule) {
	t.Helper()
	if err == nil || !errors.Is(err, okf.ErrInvalid) {
		t.Fatalf("error = %v; want OKF violation", err)
	}
	var violation *okf.Violation
	if !errors.As(err, &violation) {
		t.Fatalf("error type = %T; want *okf.Violation", err)
	}
	if violation.Path != path || violation.Rule != rule {
		t.Fatalf("violation = %#v; want path %q rule %q", violation, path, rule)
	}
}
