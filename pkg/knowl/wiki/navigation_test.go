package wiki

import (
	"reflect"
	"testing"
)

func TestInspectNavigation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, body, rule string
		targets          []string
	}{
		{name: "relative", body: "[Same](two.md#part) [Nested](nested/three.md) [Cross](../entities/four.md?view=1#part)", targets: []string{"concepts/two", "concepts/nested/three", "entities/four"}},
		{name: "encoded", body: "[Unicode](%D0%9E%D0%B1%D0%B7%D0%BE%D1%80.md) [Punctuation](a\\(b\\).md) [Space](<a b.md>)", targets: []string{"concepts/Обзор", "concepts/a(b)", "concepts/a b"}},
		{name: "reference", body: "[Two][ref]\n\n[ref]: two.md \"title\"", targets: []string{"concepts/two"}},
		{name: "external", body: "[URL](https://example.test/a.md) <https://example.test> [Asset](image.png) [Anchor](#part)"},
		{name: "literals", body: "`[[inline]]`\n\n    [[indented]]\n    [Example](code.md)\n\n```md\n[[fenced]]\n```\n\n[External](https://example.test/[[destination]])"},
		{name: "escaped", body: `\[[literal]]`},
		{name: "wiki", body: "See [[concepts/two|Two]].", rule: "link.nonportable"},
		{name: "wiki emphasized", body: "See [[**concepts/two**]].", rule: "link.nonportable"},
		{name: "unterminated", body: "See [[concepts/two", rule: "link.nonportable"},
		{name: "root", body: "[Two](/concepts/two.md)", rule: "link.nonportable"},
		{name: "extensionless", body: "[Two](two)", rule: "link.nonportable"},
		{name: "escape", body: "[Escape](../../outside.md)", rule: "link.unsafe"},
		{name: "encoded escape", body: "[Escape](%2e%2e/%2e%2e/outside.md)", rule: "link.unsafe"},
		{name: "encoded slash", body: "[Escape](..%2f..%2foutside.md)", rule: "link.unsafe"},
		{name: "encoded backslash", body: "[Escape](a%5Cb.md)", rule: "link.unsafe"},
		{name: "missing is caller concern", body: "[Missing](missing.md)", targets: []string{"concepts/missing"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := InspectNavigation("concepts/one.md", tc.body, 64)
			if got.Rule != tc.rule {
				t.Fatalf("rule = %q, want %q", got.Rule, tc.rule)
			}
			if len(got.Targets) != len(tc.targets) || len(tc.targets) > 0 && !reflect.DeepEqual(got.Targets, tc.targets) {
				t.Fatalf("targets = %#v, want %#v", got.Targets, tc.targets)
			}
		})
	}
}

func TestInspectNavigationBound(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{0, 1} {
		got := InspectNavigation("one.md", "[One](one.md) [Two](two.md)", limit)
		if got.Rule != "link.limit" {
			t.Fatalf("rule = %q", got.Rule)
		}
	}
}
