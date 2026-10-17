package main

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
)

func TestNativeRetrievalDocumentationLinks(t *testing.T) {
	repo := testRepoRoot(t)
	for _, relative := range []string{"README.md", "docs/search.md", "docs/postgres.md", "docs/local.md", "docs/design.md"} {
		filename := filepath.Join(repo, relative)
		source, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		if err := ast.Walk(parser.New().Parse(source), func(node ast.Node, entering bool) (ast.WalkStatus, error) {
			if link, ok := node.(*ast.Link); ok && entering {
				target, err := url.Parse(link.Destination.Value(source))
				if err != nil {
					return ast.WalkStop, err
				}
				if target.Scheme == "" && target.Host == "" && target.Path != "" {
					if _, err := os.Stat(filepath.Join(filepath.Dir(filename), filepath.FromSlash(target.Path))); err != nil {
						return ast.WalkStop, err
					}
				}
			}
			return ast.WalkContinue, nil
		}); err != nil {
			t.Fatalf("parsed links in%s:%v", relative, err)
		}
	}
}

func TestNativeRetrievalDocumentationConfigExamples(t *testing.T) {
	const passagePrefix = "passage: "
	const queryPrefix = "query: "
	repo := testRepoRoot(t)
	for _, filename := range []string{"search.md", "postgres.md"} {
		t.Run(filename, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join(repo, "docs", filename))
			if err != nil {
				t.Fatal(err)
			}
			var examples [][]byte
			if err := ast.Walk(parser.New().Parse(source), func(node ast.Node, entering bool) (ast.WalkStatus, error) {
				if block, ok := node.(*ast.CodeBlock); ok && entering {
					if language, _ := block.Language(source); language == "yaml" {
						examples = append(examples, block.Value.Bytes(source))
					}
				}
				return ast.WalkContinue, nil
			}); err != nil {
				t.Fatal(err)
			}
			if len(examples) != 1 {
				t.Fatalf("typed config examples=%d want1", len(examples))
			}
			working := t.TempDir()
			t.Chdir(working)
			clearKnowlEnv(t)
			directory := filepath.Join(working, ".config", appName)
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "config.yaml"), examples[0], 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, err := loadConfig(context.Background(), "", "")
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := configFromContext(ctx)
			if err != nil {
				t.Fatal(err)
			}
			storage, err := storageSettings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if filename == "postgres.md" {
				if storage.Driver != knowl.StorePostgres || storage.DSN != "postgres://KNOWL_RUNTIME@DB_HOST/KNOWL_DB?sslmode=verify-full" || loaded.Document.Knowl.Storage.SQLite != nil {
					t.Fatalf("storage=%+v", storage)
				}
			} else {
				e := loaded.Document.Knowl.Embeddings
				if !e.Enabled || e.Dimensions != 768 || e.Model != "intfloat/multilingual-e5-base" || e.Revision != "d128750597153bb5987e10b1c3493a34e5a4502a" || e.QueryPrefix != queryPrefix || e.PassagePrefix != passagePrefix || e.FailurePolicy != app.EmbeddingFallbackLexical || storage.Driver != knowl.StoreSQLite {
					t.Fatalf("embedding=%+v storage=%+v", e, storage)
				}
			}
		})
	}
}
