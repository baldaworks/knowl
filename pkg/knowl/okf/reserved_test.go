package okf_test

import (
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
)

func TestRootIndexVersionObservationAndRendering(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		content   string
		observed  string
		effective string
	}{
		{name: "missing", content: "# Bundle\n\n* [Page](page.md)\n", effective: okf.Version},
		{name: "current", content: "---\nokf_version: \"0.2\"\n---\n# Bundle\n", observed: "0.2", effective: "0.2"},
		{name: "future best effort", content: "---\nokf_version: \"0.9\"\n---\n# Bundle\n", observed: "0.9", effective: "0.9"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			index, err := okf.ParseRootIndex([]byte(test.content), okf.DefaultLimits())
			if err != nil {
				t.Fatalf("ParseRootIndex() error = %v", err)
			}
			if index.ObservedVersion != test.observed || index.EffectiveVersion() != test.effective {
				t.Fatalf("version = %q/%q", index.ObservedVersion, index.EffectiveVersion())
			}
			rendered, err := okf.RenderIndex("index.md", index, okf.DefaultLimits())
			if err != nil {
				t.Fatalf("RenderIndex() error = %v", err)
			}
			again, err := okf.RenderIndex("index.md", index, okf.DefaultLimits())
			if err != nil || string(rendered) != string(again) {
				t.Fatalf("RenderIndex() is not deterministic: %v", err)
			}
		})
	}
}

func TestIndexViolations(t *testing.T) {
	t.Parallel()

	_, err := okf.ValidateIndex("nested/index.md", []byte("---\nokf_version: \"0.2\"\n---\n# Nested\n"), okf.DefaultLimits())
	assertViolation(t, err, "nested/index.md", okf.RuleIndexInvalid)
	_, err = okf.ParseRootIndex([]byte("intro before heading\n# Bundle\n"), okf.DefaultLimits())
	assertViolation(t, err, "index.md", okf.RuleIndexInvalid)
	_, err = okf.ParseRootIndex([]byte("---\nokf_version: \"0.2\"\nextra: true\n---\n# Bundle\n"), okf.DefaultLimits())
	assertViolation(t, err, "index.md", okf.RuleIndexInvalid)
	_, err = okf.ParseRootIndex([]byte("# Bundle\narbitrary paragraph\n"), okf.DefaultLimits())
	assertViolation(t, err, "index.md", okf.RuleIndexInvalid)
	_, err = okf.ParseRootIndex([]byte("# Bundle\n- bare/page\n"), okf.DefaultLimits())
	assertViolation(t, err, "index.md", okf.RuleIndexInvalid)
	_, err = okf.RenderIndex("nested/index.md", okf.Index{ObservedVersion: okf.Version, Body: "# Nested\n"}, okf.DefaultLimits())
	assertViolation(t, err, "nested/index.md", okf.RuleIndexInvalid)
	if _, err = okf.ParseRootIndex([]byte("# Bundle\n* [Page](page.md)\n  continued description\n"), okf.DefaultLimits()); err != nil {
		t.Fatalf("ParseRootIndex(continuation) error = %v", err)
	}
}

func TestLogParseAndDeterministicRender(t *testing.T) {
	t.Parallel()

	content := []byte(`# Directory Update Log

## 2026-05-22
* **Update**: Added a concept.
- Another valid entry.

## 2026-05-15
* Initialized the bundle.
`)
	logDocument, err := okf.ValidateLog("nested/log.md", content, okf.DefaultLimits())
	if err != nil {
		t.Fatalf("ValidateLog() error = %v", err)
	}
	if logDocument.Title != "Directory Update Log" || len(logDocument.Groups) != 2 || len(logDocument.Groups[0].Entries) != 2 {
		t.Fatalf("unexpected log: %#v", logDocument)
	}
	rendered, err := okf.RenderLog("nested/log.md", logDocument, okf.DefaultLimits())
	if err != nil {
		t.Fatalf("RenderLog() error = %v", err)
	}
	again, err := okf.RenderLog("nested/log.md", logDocument, okf.DefaultLimits())
	if err != nil || string(rendered) != string(again) {
		t.Fatalf("RenderLog() is not deterministic: %v", err)
	}
}

func TestEmptyLogIsValid(t *testing.T) {
	t.Parallel()

	logDocument, err := okf.ValidateLog("log.md", []byte("# Knowl Update Log\n"), okf.DefaultLimits())
	if err != nil {
		t.Fatalf("ValidateLog(empty) error = %v", err)
	}
	if logDocument.Title != "Knowl Update Log" || len(logDocument.Groups) != 0 {
		t.Fatalf("unexpected empty log: %#v", logDocument)
	}
	if _, err := okf.RenderLog("log.md", logDocument, okf.DefaultLimits()); err != nil {
		t.Fatalf("RenderLog(empty) error = %v", err)
	}
}

func TestLogViolations(t *testing.T) {
	t.Parallel()

	tests := []string{
		"# Log\n## 2026-5-22\n* entry\n",
		"# Log\n## 2026-05-15\n* old\n## 2026-05-22\n* newer\n",
		"# Log\n## 2026-05-22\nnot a list\n",
		"# Log\n## 2026-05-22\n",
		"---\ntype: Log\n---\n# Log\n## 2026-05-22\n* entry\n",
	}
	for _, content := range tests {
		_, err := okf.ValidateLog("log.md", []byte(content), okf.DefaultLimits())
		assertViolation(t, err, "log.md", okf.RuleLogInvalid)
	}

	_, err := okf.RenderLog("log.md", okf.Log{
		Title:  "Log",
		Groups: []okf.LogGroup{{Date: time.Date(2026, 5, 22, 1, 0, 0, 0, time.UTC), Entries: []string{"entry"}}},
	}, okf.DefaultLimits())
	if err != nil {
		t.Fatalf("RenderLog() date normalization error = %v", err)
	}
}
