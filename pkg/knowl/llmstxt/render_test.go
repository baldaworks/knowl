package llmstxt_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/llmstxt"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestRenderProjectsNestedCatalogsAndDirectPages(t *testing.T) {
	input := fixtureInput()
	got, err := llmstxt.Render(context.Background(), input, llmstxt.Options{Summary: "  Agent\n navigation  "})
	if err != nil {
		t.Fatal(err)
	}
	want := "# Example Knowledge\n\n> Agent navigation\n\n## Architecture\n- [API \\[contract\\]](concepts/API%20%28v2%29.md): Stable public API\n- [Storage](concepts/storage.md)\n\n## Operations\n- [Runbook](operations/%D0%B0%D0%B2%D0%B0%D1%80%D0%B8%D1%8F.md): On-call steps\n\n## Docs\n- [Welcome](welcome.md): Start here\n"
	if string(got) != want {
		t.Fatalf("Render() =\n%s\nwant:\n%s", got, want)
	}
	again, err := llmstxt.Render(context.Background(), input, llmstxt.Options{Summary: "  Agent\n navigation  "})
	if err != nil || string(again) != string(got) {
		t.Fatalf("second Render() = %q, %v", again, err)
	}
}

func TestRenderAcceptsFullyOverlappingCatalog(t *testing.T) {
	input := fixtureInput()
	input.Catalogs[1].Content = "# Operations\n- [Secondary API](../../concepts/API%20%28v2%29.md)\n"
	input.Pages = append(input.Pages[:2], input.Pages[3:]...)

	got, err := llmstxt.Render(context.Background(), input, llmstxt.Options{})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if strings.Count(text, "concepts/API%20%28v2%29.md") != 1 {
		t.Fatalf("overlapping page emission = %q", text)
	}
	if strings.Contains(text, "## Operations") {
		t.Fatalf("empty deduplicated section was emitted: %q", text)
	}
	again, err := llmstxt.Render(context.Background(), input, llmstxt.Options{})
	if err != nil || string(again) != text {
		t.Fatalf("second Render() = %q, %v", again, err)
	}
}

func TestRenderRejectsStructurallyEmptyCatalog(t *testing.T) {
	input := llmstxt.Input{
		Index: catalog("wiki/index.md", "Empty child", "- [Empty](catalogs/empty/index.md)\n"),
		Catalogs: []knowl.PageSnapshot{
			catalog("wiki/catalogs/empty/index.md", "Empty", ""),
		},
	}
	got, err := llmstxt.Render(context.Background(), input, llmstxt.Options{})
	if !errors.Is(err, llmstxt.ErrInvalidGraph) || got != nil {
		t.Fatalf("Render() = %q, %v", got, err)
	}
}

func TestRenderUsesAbsoluteBasePathAndOverridesTitle(t *testing.T) {
	got, err := llmstxt.Render(context.Background(), fixtureInput(), llmstxt.Options{Title: "Published", BaseURL: "https://docs.example.test/knowledge/"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.HasPrefix(text, "# Published\n") || !strings.Contains(text, "(https://docs.example.test/knowledge/concepts/API%20%28v2%29.md)") {
		t.Fatalf("Render() = %s", text)
	}
}

func TestRenderRejectsInvalidGraphs(t *testing.T) {
	tests := []struct {
		name string
		edit func(*llmstxt.Input)
	}{
		{name: "missing", edit: func(input *llmstxt.Input) { input.Index.Content += "\n- [Missing](missing.md)\n" }},
		{name: "external", edit: func(input *llmstxt.Input) { input.Index.Content += "\n- [Outside](https://example.test/a.md)\n" }},
		{name: "cycle", edit: func(input *llmstxt.Input) { input.Catalogs[2].Content += "\n- [Back](../index.md)\n" }},
		{name: "orphan page", edit: func(input *llmstxt.Input) { input.Pages = append(input.Pages, page("wiki/orphan.md", "Orphan", "")) }},
		{name: "orphan catalog", edit: func(input *llmstxt.Input) {
			input.Catalogs = append(input.Catalogs, catalog("wiki/catalogs/orphan/index.md", "Orphan", ""))
		}},
		{name: "duplicate section", edit: func(input *llmstxt.Input) { input.Catalogs[1].Title = " architecture " }},
		{name: "docs collision", edit: func(input *llmstxt.Input) { input.Catalogs[0].Title = "Docs" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := fixtureInput()
			test.edit(&input)
			got, err := llmstxt.Render(context.Background(), input, llmstxt.Options{})
			if !errors.Is(err, llmstxt.ErrInvalidGraph) || got != nil {
				t.Fatalf("Render() = %q, %v", got, err)
			}
		})
	}
}

func TestRenderRejectsInvalidMetadataURLsAndLimits(t *testing.T) {
	t.Run("nil metadata", func(t *testing.T) {
		input := fixtureInput()
		input.Pages[0].OKF = nil
		_, err := llmstxt.Render(context.Background(), input, llmstxt.Options{})
		if !errors.Is(err, llmstxt.ErrInvalidInput) {
			t.Fatalf("error = %v", err)
		}
	})
	for _, baseURL := range []string{"ftp://example.test/", "https://user@example.test/", "https://example.test/?token=x", "//example.test/path"} {
		t.Run(baseURL, func(t *testing.T) {
			_, err := llmstxt.Render(context.Background(), fixtureInput(), llmstxt.Options{BaseURL: baseURL})
			if !errors.Is(err, llmstxt.ErrInvalidInput) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	t.Run("page count", func(t *testing.T) {
		limits := llmstxt.DefaultLimits()
		limits.MaxPages = 3
		_, err := llmstxt.Render(context.Background(), fixtureInput(), llmstxt.Options{Limits: limits})
		if !errors.Is(err, llmstxt.ErrLimitExceeded) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("exact and exceeded edges", func(t *testing.T) {
		limits := llmstxt.DefaultLimits()
		limits.MaxEdges = 8
		if _, err := llmstxt.Render(context.Background(), fixtureInput(), llmstxt.Options{Limits: limits}); err != nil {
			t.Fatalf("exact boundary error = %v", err)
		}
		limits.MaxEdges--
		_, err := llmstxt.Render(context.Background(), fixtureInput(), llmstxt.Options{Limits: limits})
		if !errors.Is(err, llmstxt.ErrLimitExceeded) {
			t.Fatalf("over boundary error = %v", err)
		}
	})
	t.Run("exact and exceeded depth", func(t *testing.T) {
		limits := llmstxt.DefaultLimits()
		limits.MaxDepth = 3
		if _, err := llmstxt.Render(context.Background(), fixtureInput(), llmstxt.Options{Limits: limits}); err != nil {
			t.Fatalf("exact boundary error = %v", err)
		}
		limits.MaxDepth--
		_, err := llmstxt.Render(context.Background(), fixtureInput(), llmstxt.Options{Limits: limits})
		if !errors.Is(err, llmstxt.ErrLimitExceeded) {
			t.Fatalf("over boundary error = %v", err)
		}
	})
	t.Run("catalog bytes", func(t *testing.T) {
		input := fixtureInput()
		limits := llmstxt.DefaultLimits()
		limits.MaxTextBytes = len(input.Index.Content) - 1
		_, err := llmstxt.Render(context.Background(), input, llmstxt.Options{Limits: limits})
		if !errors.Is(err, llmstxt.ErrLimitExceeded) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("output bytes", func(t *testing.T) {
		limits := llmstxt.DefaultLimits()
		limits.MaxOutputBytes = 8
		got, err := llmstxt.Render(context.Background(), fixtureInput(), llmstxt.Options{Limits: limits})
		if !errors.Is(err, llmstxt.ErrLimitExceeded) || got != nil {
			t.Fatalf("Render() = %q, %v", got, err)
		}
	})
}

func TestRenderHonorsCancellationAndMinimalInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := llmstxt.Render(ctx, fixtureInput(), llmstxt.Options{})
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("Render() = %q, %v", got, err)
	}
	minimal := llmstxt.Input{Index: catalog("wiki/index.md", "Minimal", "")}
	got, err = llmstxt.Render(context.Background(), minimal, llmstxt.Options{})
	if err != nil || string(got) != "# Minimal\n" {
		t.Fatalf("Render() = %q, %v", got, err)
	}
}

func fixtureInput() llmstxt.Input {
	return llmstxt.Input{
		Index: catalog("wiki/index.md", "Example Knowledge", "- [Architecture](catalogs/architecture/index.md)\n- [Operations](catalogs/operations/index.md)\n- [Welcome](welcome.md)\n"),
		Catalogs: []knowl.PageSnapshot{
			catalog("wiki/catalogs/architecture/index.md", "Architecture", "- [API](../../concepts/API%20%28v2%29.md)\n- [Deep](deep/index.md)\n"),
			catalog("wiki/catalogs/operations/index.md", "Operations", "- [Runbook](../../operations/%D0%B0%D0%B2%D0%B0%D1%80%D0%B8%D1%8F.md)\n- [Secondary API](../../concepts/API%20%28v2%29.md)\n"),
			catalog("wiki/catalogs/architecture/deep/index.md", "Deep", "- [Storage](../../../concepts/storage.md)\n"),
		},
		Pages: []knowl.PageSnapshot{
			page("wiki/concepts/API (v2).md", "API [contract]", "Stable public API"),
			page("wiki/concepts/storage.md", "Storage", ""),
			page("wiki/operations/авария.md", "Runbook", "On-call steps"),
			page("wiki/welcome.md", "Welcome", "Start here"),
		},
	}
}

func catalog(path, title, links string) knowl.PageSnapshot {
	return knowl.PageSnapshot{Path: path, Title: title, Content: "# " + title + "\n" + links}
}

func page(path, title, description string) knowl.PageSnapshot {
	return knowl.PageSnapshot{Path: path, Title: title, OKF: &okf.Metadata{Type: "Reference", Title: title, Description: description}}
}
