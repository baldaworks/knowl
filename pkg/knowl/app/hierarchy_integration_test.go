package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	hierarchyControlBeacon    = "CatalogControlMarkerUnique"
	scaleBusinessCatalog      = "wiki/catalogs/business/index.md"
	scalePlatformCatalog      = "wiki/catalogs/platform/index.md"
	scaleIdentityCatalog      = "wiki/catalogs/business/identity/index.md"
	scaleCommerceCatalog      = "wiki/catalogs/business/commerce/index.md"
	scaleDevicesCatalog       = "wiki/catalogs/platform/devices/index.md"
	scaleReliabilityCatalog   = "wiki/catalogs/platform/reliability/index.md"
	scaleCrossCuttingPagePath = "wiki/предметы/идентификация/раздел-00/документ-00.md"
	scaleSubjectTagPrefix     = "subject-"
	scaleTechnologyTagPrefix  = "technology-"
	scaleSubjectIdentity      = "identity"
	scaleSubjectCommerce      = "commerce"
	scaleSubjectDevices       = "devices"
	scaleSubjectReliability   = "reliability"
)

func TestHierarchyValeraScaleUnicodeMultiSourceRebuildAndExport(t *testing.T) {
	ctx := context.Background()
	workspace, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new scale workspace: %v", err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatalf("init scale workspace: %v", err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(workspace.Root(), ".knowl", "state.db"))
	if err != nil {
		t.Fatalf("open scale store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sourceA := scaleSourceEnvelope("valera-engineering", "engineering-wiki", "architecture/Обзор.md", []byte("engineering evidence"))
	sourceB := scaleSourceEnvelope("valera-product", "roadmap-wiki", "roadmap/План.md", []byte("product evidence"))
	if _, err := workspace.AcceptSource(ctx, sourceB); err != nil {
		t.Fatalf("accept supporting source: %v", err)
	}
	schema, err := workspace.Schema(ctx, testSourceScope)
	if err != nil {
		t.Fatalf("read scale schema: %v", err)
	}
	refA := "fixture:valera-engineering@r1"
	refB := "fixture:valera-product@r1"
	edits := scalePageEdits(refA, refB, 30)
	maintainer := &countingMaintainer{plan: knowl.ModelEditPlan{
		SchemaDigest: schema.Digest, SourceRefs: []string{refA, refB}, Edits: edits,
	}}
	planLimits := app.DefaultPlanLimits()
	planLimits.MaxFiles = 64
	ingest, err := app.NewIngestService(workspace, store, store, maintainer, app.IngestOptions{
		AutoApply: true, PlanLimits: planLimits,
	})
	if err != nil {
		t.Fatalf("new scale ingest service: %v", err)
	}
	if result, err := ingest.Ingest(ctx, sourceA); err != nil || result.Operation.Status != knowl.StatusCommitted {
		t.Fatalf("seed flat scale workspace = %#v, %v", result.Operation, err)
	}

	protected := hierarchyProtectedSnapshot(t, workspace)
	planner := &scaleHierarchyMaintainer{}
	hierarchy, err := app.NewHierarchyService(workspace, store, store, planner, app.HierarchyOptions{})
	if err != nil {
		t.Fatalf("new scale hierarchy service: %v", err)
	}
	result, err := hierarchy.Reconcile(ctx, testSourceScope)
	if err != nil {
		t.Fatalf("reconcile scale hierarchy: %v", err)
	}
	if result.Operation.Status != knowl.StatusCommitted || result.Commit == nil {
		t.Fatalf("scale hierarchy result = %#v", result)
	}
	if err := workspace.Validate(); err != nil {
		t.Fatalf("validate scale hierarchy: %v", err)
	}
	assertHierarchyProtectedSnapshot(t, workspace, protected)
	if _, err := os.Stat(filepath.Join(workspace.Root(), "wiki", "sources")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source mirror stat = %v, want absent", err)
	}

	inspection, err := workspace.Inspect(ctx, testSourceScope)
	if err != nil {
		t.Fatalf("inspect scale hierarchy: %v", err)
	}
	if len(inspection.Snapshot.Pages) != len(edits) || len(inspection.Catalogs) < 4 {
		t.Fatalf("scale inspection = %d pages, %d catalogs", len(inspection.Snapshot.Pages), len(inspection.Catalogs))
	}
	root := inspection.Index.Content
	if !strings.Contains(root, "/catalogs/business/index.md") || !strings.Contains(root, "/catalogs/platform/index.md") ||
		strings.Contains(root, "/архитектура/подсистемы/") || strings.Contains(root, "/продукт/дорожная-карта/") {
		t.Fatalf("scale root is not nested and semantic:\n%s", root)
	}
	capturedInput := planner.input()
	encodedInput, err := json.Marshal(capturedInput)
	if err != nil {
		t.Fatalf("encode captured hierarchy input: %v", err)
	}
	for _, forbidden := range []string{"valera-engineering", "valera-product", "source_refs", "---"} {
		if strings.Contains(string(encodedInput), forbidden) {
			t.Fatalf("hierarchy input leaked %q", forbidden)
		}
	}
	assertScaleSubjectsMixKindsAndTechnologies(t, capturedInput)
	planned := planner.plan()
	assertScaleCrossCuttingMembership(t, planned)
	for _, catalog := range planned.Catalogs {
		for _, forbidden := range []string{"valera-engineering", "valera-product", "engineering-wiki", "roadmap-wiki"} {
			if strings.Contains(strings.ToLower(catalog.Path+" "+catalog.Title), forbidden) {
				t.Fatalf("generated catalog leaked source signal %q: %#v", forbidden, catalog)
			}
		}
	}

	query, err := app.NewQueryService(workspace, store, store, ingest, app.QueryOptions{})
	if err != nil {
		t.Fatalf("new scale query service: %v", err)
	}
	beforeRebuild, err := query.Search(ctx, testSourceScope, "multisourcebeacon", knowl.ReadLimits{Pages: 5}, nil)
	if err != nil || len(beforeRebuild) != 1 {
		t.Fatalf("search synthesized page = %#v, %v", beforeRebuild, err)
	}
	reference := beforeRebuild[0]
	if len(reference.SourceDocuments) != 2 || !strings.Contains(reference.Snippet, "multisourcebeacon") ||
		strings.Contains(reference.Snippet, "source_refs") || strings.Contains(reference.Snippet, "---") {
		t.Fatalf("synthesized reference = %#v", reference)
	}
	for _, sourceID := range []knowl.SourceID{"engineering-wiki", "roadmap-wiki"} {
		filtered, filterErr := query.Search(ctx, testSourceScope, "multisourcebeacon", knowl.ReadLimits{Pages: 5}, []knowl.SourceID{sourceID})
		if filterErr != nil || len(filtered) != 1 || filtered[0].ID != reference.ID {
			t.Fatalf("source-filtered search %q = %#v, %v", sourceID, filtered, filterErr)
		}
	}
	controls, err := query.Search(ctx, testSourceScope, hierarchyControlBeacon, knowl.ReadLimits{Pages: 10}, nil)
	if err != nil || len(controls) != 0 {
		t.Fatalf("catalog controls entered retrieval = %#v, %v", controls, err)
	}
	for _, reserved := range []knowl.PageID{"index", "log", "catalogs/business/index", "catalogs/business/identity/index"} {
		if _, err := query.Page(ctx, testSourceScope, reserved, knowl.ReadLimits{Pages: 1}); !errors.Is(err, app.ErrPageNotFound) {
			t.Fatalf("reserved control %q page error = %v", reserved, err)
		}
	}

	snapshot, err := workspace.Snapshot(ctx, testSourceScope)
	if err != nil {
		t.Fatalf("snapshot scale hierarchy: %v", err)
	}
	rebuilt, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "rebuilt.db"))
	if err != nil {
		t.Fatalf("open rebuilt projection: %v", err)
	}
	t.Cleanup(func() { _ = rebuilt.Close() })
	if err := rebuilt.Rebuild(ctx, snapshot); err != nil {
		t.Fatalf("rebuild projection from canonical wiki: %v", err)
	}
	rebuiltQuery, err := app.NewQueryService(workspace, store, rebuilt, nil, app.QueryOptions{})
	if err != nil {
		t.Fatalf("new rebuilt query service: %v", err)
	}
	afterRebuild, err := rebuiltQuery.Search(ctx, testSourceScope, "multisourcebeacon", knowl.ReadLimits{Pages: 5}, nil)
	if err != nil || !reflect.DeepEqual(afterRebuild, beforeRebuild) {
		t.Fatalf("rebuilt search = %#v, %v, want %#v", afterRebuild, err, beforeRebuild)
	}

	exported := exportWikiBundle(t, workspace)
	if err := exported.Validate(); err != nil {
		t.Fatalf("validate exported wiki bundle: %v", err)
	}
	exportedSnapshot, err := exported.Snapshot(ctx, testSourceScope)
	if err != nil || len(exportedSnapshot.Pages) != len(snapshot.Pages) {
		t.Fatalf("exported snapshot = %d pages, %v", len(exportedSnapshot.Pages), err)
	}

	digestBeforeReplay, err := workspace.HierarchySnapshotDigest(ctx, testSourceScope)
	if err != nil {
		t.Fatalf("digest scale hierarchy: %v", err)
	}
	replay, err := hierarchy.Reconcile(ctx, testSourceScope)
	if err != nil || replay.Operation.Status != knowl.StatusCommitted || replay.Commit != nil {
		t.Fatalf("scale replay = %#v, %v", replay, err)
	}
	digestAfterReplay, err := workspace.HierarchySnapshotDigest(ctx, testSourceScope)
	if err != nil || digestAfterReplay != digestBeforeReplay {
		t.Fatalf("scale replay digest = %q, %v, want %q", digestAfterReplay, err, digestBeforeReplay)
	}
}

type scaleHierarchyMaintainer struct {
	mu       sync.Mutex
	captured knowl.HierarchyInput
	planned  knowl.HierarchyModelPlan
}

func (maintainer *scaleHierarchyMaintainer) PlanHierarchy(_ context.Context, input knowl.HierarchyInput) (knowl.HierarchyModelPlan, error) {
	groups := map[string][]string{scaleSubjectIdentity: {}, scaleSubjectCommerce: {}, scaleSubjectDevices: {}, scaleSubjectReliability: {}}
	for _, page := range input.Pages {
		matched := false
		for _, tag := range page.Tags {
			subject := strings.TrimPrefix(tag, scaleSubjectTagPrefix)
			if subject == tag {
				continue
			}
			if _, exists := groups[subject]; !exists {
				continue
			}
			groups[subject] = append(groups[subject], page.Path)
			matched = true
		}
		if !matched {
			return knowl.HierarchyModelPlan{}, fmt.Errorf("fixture page %q has no subject tag", page.Path)
		}
	}
	catalogs := []knowl.HierarchyCatalogSpec{
		{Path: "wiki/index.md", Title: "Knowl", Children: []string{scaleBusinessCatalog, scalePlatformCatalog}},
		{Path: scaleBusinessCatalog, Title: "Business " + hierarchyControlBeacon, Children: []string{scaleIdentityCatalog, scaleCommerceCatalog}},
		{Path: scalePlatformCatalog, Title: "Platform " + hierarchyControlBeacon, Children: []string{scaleDevicesCatalog, scaleReliabilityCatalog}},
		{Path: scaleIdentityCatalog, Title: "Identity " + hierarchyControlBeacon, Children: groups[scaleSubjectIdentity]},
		{Path: scaleCommerceCatalog, Title: "Commerce " + hierarchyControlBeacon, Children: groups[scaleSubjectCommerce]},
		{Path: scaleDevicesCatalog, Title: "Devices " + hierarchyControlBeacon, Children: groups[scaleSubjectDevices]},
		{Path: scaleReliabilityCatalog, Title: "Reliability " + hierarchyControlBeacon, Children: groups[scaleSubjectReliability]},
	}
	plan := knowl.HierarchyModelPlan{SchemaDigest: input.SchemaDigest, SnapshotDigest: input.SnapshotDigest, Catalogs: catalogs}
	maintainer.mu.Lock()
	maintainer.captured = input
	maintainer.planned = plan
	maintainer.mu.Unlock()
	return plan, nil
}

func (maintainer *scaleHierarchyMaintainer) input() knowl.HierarchyInput {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	return maintainer.captured
}

func (maintainer *scaleHierarchyMaintainer) plan() knowl.HierarchyModelPlan {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	return maintainer.planned
}

func scaleSourceEnvelope(id, sourceID, documentID string, content []byte) knowl.SourceEnvelope {
	return knowl.SourceEnvelope{
		Scope: testSourceScope, Source: knowl.SourceRef{Adapter: testSourceAdapter, ID: id},
		Version: knowl.SourceVersion{Version: "r1", Digest: digest(content)}, MediaType: testPlainMediaType,
		SourceDocument: knowl.SourceDocument{SourceID: knowl.SourceID(sourceID), DocumentID: knowl.DocumentID(documentID), Revision: "r1", URI: "https://wiki.example.test/" + sourceID},
		Content:        content,
	}
}

func scalePageEdits(refA, refB string, count int) []knowl.FileEdit {
	edits := make([]knowl.FileEdit, 0, count)
	subjects := []struct {
		name string
		dir  string
	}{
		{name: scaleSubjectIdentity, dir: "предметы/идентификация"},
		{name: scaleSubjectCommerce, dir: "предметы/коммерция"},
		{name: scaleSubjectDevices, dir: "предметы/устройства"},
		{name: scaleSubjectReliability, dir: "предметы/надёжность"},
	}
	types := []string{"Guide", "Decision", "Reference"}
	technologies := []string{"go", "postgres", "kubernetes"}
	for index := 0; index < count; index++ {
		subject := subjects[index%len(subjects)]
		documentType := types[(index/len(subjects))%len(types)]
		technology := technologies[(index/2)%len(technologies)]
		id := fmt.Sprintf("%s/раздел-%02d/документ-%02d", subject.dir, index/len(subjects), index)
		refs := []string{refA}
		tags := []string{scaleSubjectTagPrefix + subject.name, scaleTechnologyTagPrefix + technology}
		body := fmt.Sprintf("User content beacon-%02d describes the %s subject using %s.", index, subject.name, technology)
		if index == 0 {
			refs = append(refs, refB)
			tags = append(tags, scaleSubjectTagPrefix+scaleSubjectCommerce)
			body = "multisourcebeacon combines engineering and product evidence in user content."
		}
		content := fmt.Sprintf("---\nid: %s\ntitle: Документ %02d\ntype: %s\ndescription: Семантическое описание %s %02d\ntags: [%s]\nsource_refs:\n  - %s\n---\n# Документ %02d\n\n%s\n",
			id, index, documentType, subject.name, index, strings.Join(tags, ", "), strings.Join(refs, "\n  - "), index, body)
		edits = append(edits, knowl.FileEdit{Path: "wiki/" + id + ".md", Content: []byte(content)})
	}
	return edits
}

func assertScaleSubjectsMixKindsAndTechnologies(t *testing.T, input knowl.HierarchyInput) {
	t.Helper()
	typesBySubject := make(map[string]map[string]struct{})
	technologiesBySubject := make(map[string]map[string]struct{})
	for _, page := range input.Pages {
		for _, tag := range page.Tags {
			subject := strings.TrimPrefix(tag, scaleSubjectTagPrefix)
			if subject == tag {
				continue
			}
			if typesBySubject[subject] == nil {
				typesBySubject[subject] = make(map[string]struct{})
				technologiesBySubject[subject] = make(map[string]struct{})
			}
			typesBySubject[subject][page.Type] = struct{}{}
			for _, candidate := range page.Tags {
				technology := strings.TrimPrefix(candidate, scaleTechnologyTagPrefix)
				if technology != candidate {
					technologiesBySubject[subject][technology] = struct{}{}
				}
			}
		}
	}
	for _, subject := range []string{scaleSubjectIdentity, scaleSubjectCommerce, scaleSubjectDevices, scaleSubjectReliability} {
		if len(typesBySubject[subject]) < 2 || len(technologiesBySubject[subject]) < 2 {
			t.Fatalf("subject %q does not mix kinds and technologies: types=%v technologies=%v", subject, typesBySubject[subject], technologiesBySubject[subject])
		}
	}
}

func assertScaleCrossCuttingMembership(t *testing.T, plan knowl.HierarchyModelPlan) {
	t.Helper()
	parents := 0
	for _, catalog := range plan.Catalogs {
		for _, child := range catalog.Children {
			if child == scaleCrossCuttingPagePath {
				parents++
			}
		}
	}
	if parents != 2 {
		t.Fatalf("cross-cutting page parents = %d, want 2 in plan %#v", parents, plan.Catalogs)
	}
}

func hierarchyProtectedSnapshot(t *testing.T, workspace *contentfs.Workspace) map[string]string {
	t.Helper()
	inspection, err := workspace.Inspect(context.Background(), testSourceScope)
	if err != nil {
		t.Fatalf("inspect protected scale workspace: %v", err)
	}
	result := make(map[string]string, len(inspection.Snapshot.Pages)+len(inspection.RawSources))
	for _, page := range inspection.Snapshot.Pages {
		result[page.Path] = page.Digest
	}
	for _, raw := range inspection.RawSources {
		content, readErr := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(raw.Path)))
		if readErr != nil {
			t.Fatalf("read protected raw %q: %v", raw.Path, readErr)
		}
		result[raw.Path] = digest(content)
	}
	return result
}

func assertHierarchyProtectedSnapshot(t *testing.T, workspace *contentfs.Workspace, want map[string]string) {
	t.Helper()
	for path, expected := range want {
		content, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(path)))
		if err != nil || digest(content) != expected {
			t.Fatalf("protected scale file %q changed: %v", path, err)
		}
	}
}

func exportWikiBundle(t *testing.T, source *contentfs.Workspace) *contentfs.Workspace {
	t.Helper()
	exported, err := contentfs.New(t.TempDir())
	if err != nil {
		t.Fatalf("new exported workspace: %v", err)
	}
	if err := exported.Init(); err != nil {
		t.Fatalf("init exported workspace: %v", err)
	}
	sourceRoot := filepath.Join(source.Root(), "wiki")
	err = filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil || relative == "." {
			return err
		}
		target := filepath.Join(exported.Root(), "wiki", relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o600)
	})
	if err != nil {
		t.Fatalf("copy exported wiki: %v", err)
	}
	return exported
}
