package fs

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
)

func TestExportOKFPreflightsEveryDocumentKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path, content string
		rule          okf.Rule
	}{
		{path: "nested/missing.md", content: "# No frontmatter\n", rule: okf.RuleFrontmatterMissing},
		{path: "nested/malformed.md", content: "---\ntype: [\n---\n", rule: okf.RuleFrontmatterMalformed},
		{path: "nested/untyped.md", content: "---\ntitle: Untyped\n---\n", rule: okf.RuleTypeMissing},
		{path: okfIndexFilename, content: "# Bundle\nnot catalog content\n", rule: okf.RuleIndexInvalid},
		{path: "nested/index.md", content: "---\nokf_version: '0.2'\n---\n# Nested\n", rule: okf.RuleIndexInvalid},
		{path: "log.md", content: "# Log\n## impossible date\n* Entry\n", rule: okf.RuleLogInvalid},
		{path: "nested/log.md", content: "---\ntype: Log\n---\n# Log\n", rule: okf.RuleLogInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			workspace := newExportWorkspace(t)
			writeExportFixture(t, workspace, "wiki/"+tc.path, []byte(tc.content))
			parent := t.TempDir()
			destination := filepath.Join(parent, "public")
			err := workspace.ExportOKF(t.Context(), destination, DefaultExportLimits())
			var invalid *okf.Violation
			if !errors.Is(err, okf.ErrInvalid) || !errors.As(err, &invalid) || invalid.Path != tc.path || invalid.Rule != tc.rule {
				t.Fatalf("violation: %#v %v", invalid, err)
			}
			if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("partial publication: %v", err)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("temporary artifacts: %#v %v", entries, err)
			}
		})
	}
}

func TestExportOKFStandaloneMinimalAndExtendedConcepts(t *testing.T) {
	t.Parallel()
	workspace := newExportWorkspace(t)
	inputs := map[string][]byte{
		"minimal.md":         []byte("---\ntype: Reference\n---\n[Missing](absent.md)\n"),
		"nested/extended.md": []byte("---\ntype: Future Type\ngenerated: {by: agent:test}\nverified: {by: human:reader, at: '2026-10-14T06:00:00+06:00'}\nproducer: {nested: [true, 4]}\n---\n[Minimal](../minimal.md)\n"),
		"nested/index.md":    []byte("# Nested\n* [Extended](extended.md)\n"),
		"nested/log.md":      []byte("# Nested Log\n\n## 2026-10-14\n* Added extended concept.\n"),
		"assets/blob.bin":    {0, 255, 1},
	}
	for path, data := range inputs {
		writeExportFixture(t, workspace, "wiki/"+path, data)
	}
	destination := filepath.Join(t.TempDir(), "public")
	if err := workspace.ExportOKF(t.Context(), destination, DefaultExportLimits()); err != nil {
		t.Fatal(err)
	}
	for relative, want := range inputs {
		got, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("changed exported bytes for %s", relative)
		}
		kind, err := okf.ClassifyPath(relative)
		if err != nil {
			t.Fatal(err)
		}
		switch kind {
		case okf.DocumentConcept:
			before, err := okf.ParseConcept(relative, want, okf.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			after, err := okf.ParseConcept(relative, got, okf.DefaultLimits())
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("metadata changed: %#v %v", after, err)
			}
		case okf.DocumentIndex:
			if _, err := okf.ValidateIndex(relative, got, okf.DefaultLimits()); err != nil {
				t.Fatal(err)
			}
		case okf.DocumentLog:
			if _, err := okf.ValidateLog(relative, got, okf.DefaultLimits()); err != nil {
				t.Fatal(err)
			}
		case okf.DocumentAsset:
		}
	}
}
