package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	hierarchySchemaDigest    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hierarchySnapshotDigest  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hierarchyCurrentDigest   = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	hierarchyRootTitle       = "Knowl"
	hierarchyOperatorCatalog = "wiki/operator/index.md"
	hierarchyArchitecture    = "wiki/concepts/architecture.md"
	hierarchyRoadmap         = "wiki/concepts/roadmap.md"
	hierarchyGlossary        = "wiki/concepts/Глоссарий-проекта.md"
	hierarchyArchitectureCat = "wiki/catalogs/architecture/index.md"
	hierarchyProductCat      = "wiki/catalogs/product/index.md"
	hierarchySourceCatalog   = "wiki/sources/source-a/index.md"
	hierarchySourceTerm      = "fastronome-wiki"
	hierarchyArchitectureTag = "Architecture"
)

func TestValidateHierarchyPlanRendersDeterministicSemanticCatalogs(t *testing.T) {
	t.Parallel()

	input, model := hierarchyFixture()
	first, err := ValidateHierarchyPlan(context.Background(), input, model, HierarchyValidationOptions{})
	if err != nil {
		t.Fatalf("ValidateHierarchyPlan() error = %v", err)
	}
	second, err := ValidateHierarchyPlan(context.Background(), input, model, HierarchyValidationOptions{})
	if err != nil {
		t.Fatalf("ValidateHierarchyPlan() second error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("validated plans differ:\nfirst: %#v\nsecond: %#v", first, second)
	}
	firstDigest, err := HierarchyPlanDigest(first)
	if err != nil {
		t.Fatalf("HierarchyPlanDigest(first): %v", err)
	}
	secondDigest, err := HierarchyPlanDigest(second)
	if err != nil || firstDigest != secondDigest || len(firstDigest) != 64 {
		t.Fatalf("plan digests = %q/%q, err=%v", firstDigest, secondDigest, err)
	}
	if len(first.Catalogs) != 3 || first.Catalogs[0].Path != hierarchyRootPath || len(first.Mutations) != 3 {
		t.Fatalf("normalized plan = %#v", first)
	}

	contents := hierarchyMutationContents(first.Mutations)
	root := contents[hierarchyRootPath]
	parsedRoot, err := okf.ParseRootIndex(root, okf.DefaultLimits())
	if err != nil {
		t.Fatalf("parse rendered root: %v\n%s", err, root)
	}
	if parsedRoot.ObservedVersion != okf.Version || strings.Contains(string(root), "/concepts/") {
		t.Fatalf("root catalog is not nested OKF v0.2:\n%s", root)
	}
	if !strings.Contains(string(root), "catalogs/architecture/index.md") || !strings.Contains(string(root), "catalogs/product/index.md") {
		t.Fatalf("root links =\n%s", root)
	}
	product := contents[hierarchyProductCat]
	if !strings.Contains(string(product), "%D0%93%D0%BB%D0%BE%D1%81%D1%81%D0%B0%D1%80%D0%B8%D0%B9-%D0%BF%D1%80%D0%BE%D0%B5%D0%BA%D1%82%D0%B0.md") {
		t.Fatalf("Unicode destination is not deterministically escaped:\n%s", product)
	}
	for _, mutation := range first.Mutations {
		if mutation.Action != knowl.SourceMutationWrite || !IsManagedHierarchyCatalog(mutation.Path) || !strings.HasSuffix(mutation.Path, "/index.md") && mutation.Path != hierarchyRootPath {
			t.Fatalf("non-catalog mutation = %#v", mutation)
		}
	}
}

func TestValidateHierarchyPlanProducesNoOpForMatchingCatalogDigests(t *testing.T) {
	t.Parallel()

	input, model := hierarchyFixture()
	initial, err := ValidateHierarchyPlan(context.Background(), input, model, HierarchyValidationOptions{})
	if err != nil {
		t.Fatalf("initial plan: %v", err)
	}
	input.Catalogs = make([]knowl.HierarchyCatalog, 0, len(initial.Mutations))
	for _, mutation := range initial.Mutations {
		if mutation.Action != knowl.SourceMutationWrite {
			continue
		}
		title := hierarchyRootTitle
		for _, catalog := range initial.Catalogs {
			if catalog.Path == mutation.Path {
				title = catalog.Title
				break
			}
		}
		input.Catalogs = append(input.Catalogs, knowl.HierarchyCatalog{
			Path: mutation.Path, Digest: digestHierarchyBytes(mutation.Content), Title: title,
		})
	}
	replayed, err := ValidateHierarchyPlan(context.Background(), input, model, HierarchyValidationOptions{})
	if err != nil {
		t.Fatalf("replayed plan: %v", err)
	}
	if len(replayed.Mutations) != 0 {
		t.Fatalf("replayed mutations = %#v, want no-op", replayed.Mutations)
	}
}

func TestValidateHierarchyPlanAllowsSparseCrossCuttingMembership(t *testing.T) {
	t.Parallel()

	input, model := hierarchyFixture()
	architecture := catalogSpec(&model, hierarchyArchitectureCat)
	architecture.Children = append(architecture.Children, hierarchyGlossary)
	plan, err := ValidateHierarchyPlan(context.Background(), input, model, HierarchyValidationOptions{})
	if err != nil {
		t.Fatalf("ValidateHierarchyPlan() error = %v", err)
	}
	contents := hierarchyMutationContents(plan.Mutations)
	wantDestination := "%D0%93%D0%BB%D0%BE%D1%81%D1%81%D0%B0%D1%80%D0%B8%D0%B9-%D0%BF%D1%80%D0%BE%D0%B5%D0%BA%D1%82%D0%B0.md"
	for _, catalogPath := range []string{hierarchyArchitectureCat, hierarchyProductCat} {
		if !strings.Contains(string(contents[catalogPath]), wantDestination) {
			t.Fatalf("cross-cutting page is absent from %q:\n%s", catalogPath, contents[catalogPath])
		}
	}
}

func TestValidateHierarchyPlanAllowsEmptyRootOnlyForEmptyWiki(t *testing.T) {
	t.Parallel()

	input, model := hierarchyFixture()
	input.Pages = nil
	input.MinRootCatalogs = 0
	model.Catalogs = []knowl.HierarchyCatalogSpec{{Path: hierarchyRootPath, Title: hierarchyRootTitle}}
	if _, err := ValidateHierarchyPlan(context.Background(), input, model, HierarchyValidationOptions{}); err != nil {
		t.Fatalf("empty wiki root error = %v", err)
	}

	input, model = hierarchyFixture()
	input.MinRootCatalogs = 0
	model.Catalogs = []knowl.HierarchyCatalogSpec{{Path: hierarchyRootPath, Title: hierarchyRootTitle}}
	if _, err := ValidateHierarchyPlan(context.Background(), input, model, HierarchyValidationOptions{}); !errors.Is(err, ErrHierarchyPlanInvalid) {
		t.Fatalf("non-empty wiki with empty root error = %v, want %v", err, ErrHierarchyPlanInvalid)
	}
}

func TestValidateHierarchyPlanSucceedsAtLimitsAndFailsOneOver(t *testing.T) {
	t.Parallel()

	input, model := hierarchyFixture()
	baseline, err := ValidateHierarchyPlan(context.Background(), input, model, HierarchyValidationOptions{})
	if err != nil {
		t.Fatalf("baseline hierarchy plan: %v", err)
	}
	modelBytes, err := json.Marshal(model)
	if err != nil {
		t.Fatalf("marshal hierarchy model: %v", err)
	}
	maxPathBytes := 0
	maxExcerptCharacters := 0
	for _, page := range input.Pages {
		maxPathBytes = max(maxPathBytes, len(page.Path))
		maxExcerptCharacters = max(maxExcerptCharacters, utf8.RuneCountInString(page.Excerpt))
	}
	for _, catalog := range model.Catalogs {
		maxPathBytes = max(maxPathBytes, len(catalog.Path))
	}
	maxCatalogBytes := 0
	for _, mutation := range baseline.Mutations {
		maxCatalogBytes = max(maxCatalogBytes, len(mutation.Content))
	}
	edges := 0
	for _, catalog := range model.Catalogs {
		edges += len(catalog.Children)
	}
	input.Limits.MaxPages = len(input.Pages)
	input.Limits.MaxCatalogs = len(model.Catalogs)
	input.Limits.MaxEdges = edges
	input.Limits.MaxDepth = 2
	input.Limits.MaxExcerptCharacters = maxExcerptCharacters
	input.Limits.MaxPlanBytes = len(modelBytes)
	input.Limits.MaxEdits = len(baseline.Mutations)
	input.Limits.MaxPathBytes = maxPathBytes
	input.Limits.MaxCatalogBytes = maxCatalogBytes
	if _, err := ValidateHierarchyPlan(context.Background(), input, model, HierarchyValidationOptions{}); err != nil {
		t.Fatalf("exact-limit hierarchy plan: %v", err)
	}

	tests := []struct {
		name string
		edit func(*knowl.HierarchyLimits)
		want error
	}{
		{name: "pages", edit: func(limits *knowl.HierarchyLimits) { limits.MaxPages-- }, want: ErrHierarchyLimitExceeded},
		{name: "catalogs", edit: func(limits *knowl.HierarchyLimits) { limits.MaxCatalogs-- }, want: ErrHierarchyLimitExceeded},
		{name: "edges", edit: func(limits *knowl.HierarchyLimits) { limits.MaxEdges-- }, want: ErrHierarchyLimitExceeded},
		{name: "depth", edit: func(limits *knowl.HierarchyLimits) { limits.MaxDepth-- }, want: ErrHierarchyLimitExceeded},
		{name: "excerpt", edit: func(limits *knowl.HierarchyLimits) { limits.MaxExcerptCharacters-- }, want: ErrHierarchyLimitExceeded},
		{name: "plan bytes", edit: func(limits *knowl.HierarchyLimits) { limits.MaxPlanBytes-- }, want: ErrHierarchyLimitExceeded},
		{name: "edits", edit: func(limits *knowl.HierarchyLimits) { limits.MaxEdits-- }, want: ErrHierarchyLimitExceeded},
		{name: "path bytes", edit: func(limits *knowl.HierarchyLimits) { limits.MaxPathBytes-- }, want: ErrHierarchyForbiddenPath},
		{name: "catalog bytes", edit: func(limits *knowl.HierarchyLimits) { limits.MaxCatalogBytes-- }, want: ErrHierarchyLimitExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			over := input
			test.edit(&over.Limits)
			if _, err := ValidateHierarchyPlan(context.Background(), over, model, HierarchyValidationOptions{}); !errors.Is(err, test.want) {
				t.Fatalf("one-over error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestValidateHierarchyPlanEscapesUntrustedTitlesWithoutAddingLinks(t *testing.T) {
	t.Parallel()

	input, model := hierarchyFixture()
	catalogSpec(&model, hierarchyArchitectureCat).Title = `<script>Architecture](injected)</script>`
	input.Pages[0].Title = `Roadmap](injected)`
	plan, err := ValidateHierarchyPlan(context.Background(), input, model, HierarchyValidationOptions{})
	if err != nil {
		t.Fatalf("ValidateHierarchyPlan() error = %v", err)
	}
	contents := hierarchyMutationContents(plan.Mutations)
	root := string(contents[hierarchyRootPath])
	product := string(contents[hierarchyProductCat])
	if strings.Contains(root, "<script>") || strings.Contains(root, "](injected)") ||
		strings.Contains(product, "](injected)") || !strings.Contains(root, "&lt;script&gt;") ||
		!strings.Contains(product, "Roadmap&#93;(injected)") {
		t.Fatalf("untrusted titles were not escaped:\nroot:\n%s\nproduct:\n%s", root, product)
	}
}

func TestValidateHierarchyPlanDeletesOnlyObsoleteManagedCatalogs(t *testing.T) {
	t.Parallel()

	input, model := hierarchyFixture()
	input.Catalogs = append(input.Catalogs,
		knowl.HierarchyCatalog{Path: "wiki/catalogs/obsolete/index.md", Digest: strings.Repeat("d", 64), Title: "Obsolete"},
		knowl.HierarchyCatalog{Path: hierarchyOperatorCatalog, Digest: strings.Repeat("e", 64), Title: "Operator catalog"},
	)
	plan, err := ValidateHierarchyPlan(context.Background(), input, model, HierarchyValidationOptions{})
	if err != nil {
		t.Fatalf("ValidateHierarchyPlan() error = %v", err)
	}
	var obsolete, operator bool
	for _, mutation := range plan.Mutations {
		if mutation.Path == "wiki/catalogs/obsolete/index.md" {
			obsolete = mutation.Action == knowl.SourceMutationDelete && mutation.ExpectedDigest == strings.Repeat("d", 64) && len(mutation.Content) == 0
		}
		if mutation.Path == hierarchyOperatorCatalog {
			operator = true
		}
	}
	if !obsolete || operator {
		t.Fatalf("managed delete=%t operator mutation=%t, mutations=%#v", obsolete, operator, plan.Mutations)
	}
}

func TestValidateHierarchyPlanRejectsUnsafeOrIncompleteGraphs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		edit    func(*knowl.HierarchyInput, *knowl.HierarchyModelPlan, *HierarchyValidationOptions)
		wantErr error
		path    string
	}{
		{name: "schema digest mismatch", edit: func(_ *knowl.HierarchyInput, plan *knowl.HierarchyModelPlan, _ *HierarchyValidationOptions) {
			plan.SchemaDigest = strings.Repeat("f", 64)
		}, wantErr: ErrHierarchyDigestMismatch},
		{name: "missing ordinary page", edit: func(_ *knowl.HierarchyInput, plan *knowl.HierarchyModelPlan, _ *HierarchyValidationOptions) {
			catalogSpec(plan, hierarchyProductCat).Children = []string{hierarchyGlossary}
		}, wantErr: ErrHierarchyPlanInvalid, path: hierarchyRoadmap},
		{name: "catalog cycle", edit: func(_ *knowl.HierarchyInput, plan *knowl.HierarchyModelPlan, _ *HierarchyValidationOptions) {
			architecture := catalogSpec(plan, hierarchyArchitectureCat)
			architecture.Children = append(architecture.Children, hierarchyRootPath)
		}, wantErr: ErrHierarchyPlanInvalid, path: hierarchyRootPath},
		{name: "unreachable catalog", edit: func(input *knowl.HierarchyInput, plan *knowl.HierarchyModelPlan, _ *HierarchyValidationOptions) {
			input.MinRootCatalogs = 1
			catalogSpec(plan, hierarchyRootPath).Children = []string{hierarchyArchitectureCat, hierarchyRoadmap, hierarchyGlossary}
		}, wantErr: ErrHierarchyPlanInvalid, path: hierarchyProductCat},
		{name: "flat multi-domain root", edit: func(_ *knowl.HierarchyInput, plan *knowl.HierarchyModelPlan, _ *HierarchyValidationOptions) {
			plan.Catalogs = []knowl.HierarchyCatalogSpec{{Path: hierarchyRootPath, Title: hierarchyRootTitle, Children: []string{
				hierarchyArchitecture, hierarchyRoadmap, hierarchyGlossary,
			}}}
		}, wantErr: ErrHierarchyPlanInvalid, path: "semantic child catalogs"},
		{name: "broken target", edit: func(_ *knowl.HierarchyInput, plan *knowl.HierarchyModelPlan, _ *HierarchyValidationOptions) {
			architecture := catalogSpec(plan, hierarchyArchitectureCat)
			architecture.Children = append(architecture.Children, "wiki/concepts/missing.md")
		}, wantErr: ErrHierarchyPlanInvalid, path: "wiki/concepts/missing.md"},
		{name: "ordinary model target", edit: func(_ *knowl.HierarchyInput, plan *knowl.HierarchyModelPlan, _ *HierarchyValidationOptions) {
			catalogSpec(plan, hierarchyArchitectureCat).Path = "wiki/concepts/provider-edit.md"
		}, wantErr: ErrHierarchyForbiddenPath, path: "wiki/concepts/provider-edit.md"},
		{name: "source mirror namespace", edit: func(_ *knowl.HierarchyInput, plan *knowl.HierarchyModelPlan, _ *HierarchyValidationOptions) {
			catalogSpec(plan, hierarchyArchitectureCat).Path = hierarchySourceCatalog
		}, wantErr: ErrHierarchyForbiddenPath, path: hierarchySourceCatalog},
		{name: "configured source path segment", edit: func(_ *knowl.HierarchyInput, plan *knowl.HierarchyModelPlan, options *HierarchyValidationOptions) {
			catalogSpec(plan, hierarchyArchitectureCat).Path = "wiki/catalogs/" + hierarchySourceTerm + "/index.md"
			catalogSpec(plan, hierarchyRootPath).Children[1] = "wiki/catalogs/" + hierarchySourceTerm + "/index.md"
			options.ForbiddenCatalogTerms = []string{hierarchySourceTerm}
		}, wantErr: ErrHierarchyForbiddenPath, path: hierarchySourceTerm},
		{name: "configured source title", edit: func(_ *knowl.HierarchyInput, plan *knowl.HierarchyModelPlan, options *HierarchyValidationOptions) {
			catalogSpec(plan, hierarchyArchitectureCat).Title = "Fastronome-Wiki"
			options.ForbiddenCatalogTerms = []string{hierarchySourceTerm}
		}, wantErr: ErrHierarchyForbiddenPath, path: "source namespace"},
		{name: "duplicate child", edit: func(_ *knowl.HierarchyInput, plan *knowl.HierarchyModelPlan, _ *HierarchyValidationOptions) {
			architecture := catalogSpec(plan, hierarchyArchitectureCat)
			architecture.Children = append(architecture.Children, architecture.Children[0])
		}, wantErr: ErrHierarchyPlanInvalid, path: "duplicate child"},
		{name: "empty nested catalog", edit: func(_ *knowl.HierarchyInput, plan *knowl.HierarchyModelPlan, _ *HierarchyValidationOptions) {
			catalogSpec(plan, hierarchyArchitectureCat).Children = nil
		}, wantErr: ErrHierarchyPlanInvalid, path: hierarchyArchitectureCat},
		{name: "source ordinary page", edit: func(input *knowl.HierarchyInput, _ *knowl.HierarchyModelPlan, _ *HierarchyValidationOptions) {
			input.Pages[0].Path = "wiki/sources/source-a/page.md"
		}, wantErr: ErrHierarchyForbiddenPath, path: "wiki/sources/source-a/page.md"},
		{name: "ordinary path used as catalog membership", edit: func(input *knowl.HierarchyInput, _ *knowl.HierarchyModelPlan, _ *HierarchyValidationOptions) {
			input.Pages[0].Catalogs = []string{hierarchyRoadmap}
		}, wantErr: ErrHierarchyForbiddenPath, path: hierarchyRoadmap},
		{name: "configured source root title", edit: func(_ *knowl.HierarchyInput, plan *knowl.HierarchyModelPlan, options *HierarchyValidationOptions) {
			catalogSpec(plan, hierarchyRootPath).Title = "Fastronome-Wiki"
			options.ForbiddenCatalogTerms = []string{hierarchySourceTerm}
		}, wantErr: ErrHierarchyForbiddenPath, path: "source namespace"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input, model := hierarchyFixture()
			options := HierarchyValidationOptions{}
			test.edit(&input, &model, &options)
			_, err := ValidateHierarchyPlan(context.Background(), input, model, options)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if test.path != "" && !strings.Contains(err.Error(), test.path) {
				t.Fatalf("error = %q, want affected value %q", err, test.path)
			}
		})
	}
}

func TestValidateHierarchyPlanEnforcesCompleteBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		edit func(*knowl.HierarchyInput, *knowl.HierarchyModelPlan)
	}{
		{name: "page count", edit: func(input *knowl.HierarchyInput, _ *knowl.HierarchyModelPlan) { input.Limits.MaxPages = 2 }},
		{name: "catalog count", edit: func(input *knowl.HierarchyInput, _ *knowl.HierarchyModelPlan) { input.Limits.MaxCatalogs = 2 }},
		{name: "edge count", edit: func(input *knowl.HierarchyInput, _ *knowl.HierarchyModelPlan) { input.Limits.MaxEdges = 3 }},
		{name: "graph depth", edit: func(input *knowl.HierarchyInput, _ *knowl.HierarchyModelPlan) { input.Limits.MaxDepth = 1 }},
		{name: "excerpt", edit: func(input *knowl.HierarchyInput, _ *knowl.HierarchyModelPlan) {
			input.Limits.MaxExcerptCharacters = 2
			input.Pages[0].Excerpt = "three"
		}},
		{name: "input bytes", edit: func(input *knowl.HierarchyInput, _ *knowl.HierarchyModelPlan) { input.Limits.MaxInputBytes = 64 }},
		{name: "plan bytes", edit: func(input *knowl.HierarchyInput, _ *knowl.HierarchyModelPlan) { input.Limits.MaxPlanBytes = 64 }},
		{name: "edit count", edit: func(input *knowl.HierarchyInput, _ *knowl.HierarchyModelPlan) { input.Limits.MaxEdits = 2 }},
		{name: "catalog bytes", edit: func(input *knowl.HierarchyInput, _ *knowl.HierarchyModelPlan) { input.Limits.MaxCatalogBytes = 32 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input, model := hierarchyFixture()
			test.edit(&input, &model)
			_, err := ValidateHierarchyPlan(context.Background(), input, model, HierarchyValidationOptions{})
			if !errors.Is(err, ErrHierarchyLimitExceeded) {
				t.Fatalf("error = %v, want hierarchy limit", err)
			}
		})
	}
}

func TestValidateHierarchyPlanHonorsCancellation(t *testing.T) {
	t.Parallel()

	input, model := hierarchyFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ValidateHierarchyPlan(ctx, input, model, HierarchyValidationOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want canceled", err)
	}
}

func TestManagedHierarchyCatalogBoundary(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"wiki/index.md":                       true,
		"wiki/catalogs/domain/index.md":       true,
		"wiki/catalogs/домен/deep/index.md":   true,
		"wiki/catalogs/domain/page.md":        false,
		"wiki/catalogs/index.md":              true,
		"wiki/operator/index.md":              false,
		"wiki/sources/source-a/index.md":      false,
		"wiki/catalogs/../operator/index.md":  false,
		"wiki/catalogs/domain/index.md/extra": false,
	}
	for candidate, want := range tests {
		if got := IsManagedHierarchyCatalog(candidate); got != want {
			t.Errorf("IsManagedHierarchyCatalog(%q) = %t, want %t", candidate, got, want)
		}
	}
}

func hierarchyFixture() (knowl.HierarchyInput, knowl.HierarchyModelPlan) {
	input := knowl.HierarchyInput{
		Scope:           "local",
		SchemaDigest:    hierarchySchemaDigest,
		SnapshotDigest:  hierarchySnapshotDigest,
		MinRootCatalogs: 2,
		Limits:          DefaultHierarchyLimits(),
		Pages: []knowl.HierarchyPage{
			{ID: "concepts/roadmap", Path: hierarchyRoadmap, Digest: strings.Repeat("1", 64), Type: "Product", Title: "Roadmap", Tags: []string{"planning", "product"}, Excerpt: "Milestones", Catalogs: []string{hierarchyRootPath}},
			{ID: "concepts/architecture", Path: hierarchyArchitecture, Digest: strings.Repeat("2", 64), Type: hierarchyArchitectureTag, Title: hierarchyArchitectureTag, Tags: []string{"system", "architecture"}, Excerpt: "Components", Catalogs: []string{hierarchyRootPath}},
			{ID: "concepts/glossary", Path: hierarchyGlossary, Digest: strings.Repeat("3", 64), Type: "Reference", Title: "Глоссарий проекта", Tags: []string{"product", "terms"}, Excerpt: "Термины", Catalogs: []string{hierarchyRootPath}},
		},
		Catalogs: []knowl.HierarchyCatalog{
			{Path: hierarchyRootPath, Digest: hierarchyCurrentDigest, Title: hierarchyRootTitle, Children: []string{hierarchyRoadmap, hierarchyArchitecture, hierarchyGlossary}},
		},
	}
	model := knowl.HierarchyModelPlan{
		SchemaDigest:   hierarchySchemaDigest,
		SnapshotDigest: hierarchySnapshotDigest,
		Catalogs: []knowl.HierarchyCatalogSpec{
			{Path: hierarchyProductCat, Title: "Product", Children: []string{hierarchyGlossary, hierarchyRoadmap}},
			{Path: hierarchyRootPath, Title: hierarchyRootTitle, Children: []string{hierarchyProductCat, hierarchyArchitectureCat}},
			{Path: hierarchyArchitectureCat, Title: hierarchyArchitectureTag, Children: []string{hierarchyArchitecture}},
		},
	}
	return input, model
}

func hierarchyMutationContents(mutations []knowl.HierarchyMutation) map[string][]byte {
	result := make(map[string][]byte, len(mutations))
	for _, mutation := range mutations {
		if mutation.Action == knowl.SourceMutationWrite {
			result[mutation.Path] = mutation.Content
		}
	}
	return result
}

func catalogSpec(plan *knowl.HierarchyModelPlan, catalogPath string) *knowl.HierarchyCatalogSpec {
	for index := range plan.Catalogs {
		if plan.Catalogs[index].Path == catalogPath {
			return &plan.Catalogs[index]
		}
	}
	panic("missing hierarchy fixture catalog: " + catalogPath)
}
