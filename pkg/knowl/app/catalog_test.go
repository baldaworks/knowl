package app

import (
	"errors"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const mutatedFixtureValue = "mutated"

func TestBoundedCatalogsOrdersRootFirstAndEnforcesLimits(t *testing.T) {
	catalogs := []knowl.PageSnapshot{
		{Path: "wiki/zeta/index.md", Content: "# Zeta\n", Body: "# Zeta\n"},
		{Path: rootCatalogPath, Content: "# Root\n", Body: "# Root\n"},
		{Path: "wiki/alpha/index.md", Content: "# Alpha\n", Body: "# Alpha\n"},
	}
	limits := knowl.ReadLimits{Pages: 3, Bytes: 64, Characters: 64}
	got, err := boundedCatalogs(catalogs, limits)
	if err != nil {
		t.Fatalf("boundedCatalogs() error: %v", err)
	}
	if got[0].Path != rootCatalogPath || got[1].Path != "wiki/alpha/index.md" || got[2].Path != "wiki/zeta/index.md" {
		t.Fatalf("catalog order = %#v", got)
	}
	got[0].Content = mutatedFixtureValue
	if catalogs[1].Content == mutatedFixtureValue {
		t.Fatal("boundedCatalogs returned mutable caller content")
	}
	for _, limited := range []knowl.ReadLimits{
		{Pages: 2, Bytes: 64, Characters: 64},
		{Pages: 3, Bytes: 8, Characters: 64},
		{Pages: 3, Bytes: 64, Characters: 8},
	} {
		if _, err := boundedCatalogs(catalogs, limited); !errors.Is(err, ErrPlanLimitExceeded) {
			t.Fatalf("boundedCatalogs(%#v) error = %v", limited, err)
		}
	}
}
