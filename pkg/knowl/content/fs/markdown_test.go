package fs

import "testing"

func TestMarkdownTitlePrefersNormalizedFrontmatterForHeadinglessPage(t *testing.T) {
	content := []byte("---\ntitle: Глоссарий-проекта\ntype: source\n---\n\nПолезный текст без заголовка.\n")
	if got, want := markdownTitle(content), "Глоссарий-проекта"; got != want {
		t.Fatalf("markdownTitle() = %q, want %q", got, want)
	}
}
