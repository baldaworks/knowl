package knowl

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const testScope knowl.ScopeRef = "local"
const testPageOneID = "entities/one"
const testPageOnePath = "wiki/entities/one.md"
const testIndexPath = "wiki/index.md"
const testHierarchyOperation = "hierarchy-authority-operation"
const testHierarchyOldPath = "wiki/catalogs/old/index.md"
const testHierarchyArchPath = "wiki/catalogs/architecture/index.md"
const testHierarchyProdPath = "wiki/catalogs/product/index.md"
const rootIndexContent = "---\nokf_version: \"0.2\"\n---\n# Knowl\n"

func newMaintenanceTestContent(root string) (*hostMaintenanceContent, error) {
	workspace, err := contentfs.New(root)
	return &hostMaintenanceContent{Workspace: workspace}, err
}
func bindMaintenanceTest(content *hostMaintenanceContent, lifecycles app.SourceLifecycleReader, coordinator app.ApplyCoordinator) error {
	bound, err := newHostMaintenanceContent(content.Workspace, lifecycles, coordinator)
	if err == nil {
		*content = *bound
	}
	return err
}

type maintenanceLifecycleReader struct {
	authority knowl.SourceLifecycleAuthority
	states    map[knowl.SourceOwner]knowl.SourceLifecycle
}

func (reader maintenanceLifecycleReader) SourceLifecycleAuthority(ctx context.Context, scope knowl.ScopeRef) (knowl.SourceLifecycleAuthority, error) {
	if err := ctx.Err(); err != nil {
		return knowl.SourceLifecycleAuthority{}, err
	}
	if scope != reader.authority.Scope {
		return knowl.SourceLifecycleAuthority{}, app.ErrSourceLifecycleUnowned
	}
	return reader.authority, nil
}
func (reader maintenanceLifecycleReader) SourceLifecycle(ctx context.Context, scope knowl.ScopeRef, owner knowl.SourceOwner) (knowl.SourceLifecycle, error) {
	if err := ctx.Err(); err != nil {
		return knowl.SourceLifecycle{}, err
	}
	state, ok := reader.states[owner]
	if !ok || scope != reader.authority.Scope {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleUnowned
	}
	return state, nil
}
func digestBytes(content []byte) string { return fmt.Sprintf("%x", sha256.Sum256(content)) }
func validWorkspacePage(id, title, ref, body string) []byte {
	return []byte("---\nid: " + id + "\ntitle: " + title + "\ntype: entity\nsource_refs:\n  - " + ref + "\n---\n# " + title + "\n\n" + body + "\n")
}
func acceptStructuredWorkspaceSource(t *testing.T, workspace *hostMaintenanceContent, sourceID knowl.SourceID, documentID knowl.DocumentID, revision string) string {
	t.Helper()
	content := []byte(string(sourceID) + "/" + string(documentID) + "@" + revision)
	accepted, err := workspace.AcceptSource(t.Context(), knowl.SourceEnvelope{Scope: testScope, Source: knowl.SourceRef{Adapter: string(knowl.SourceAdapterFilesystem), ID: string(sourceID) + "/" + string(documentID)}, Version: knowl.SourceVersion{Version: revision, Digest: digestBytes(content)}, MediaType: lifecycleMarkdownMediaType, SourceDocument: knowl.SourceDocument{SourceID: sourceID, DocumentID: documentID, Revision: revision, URI: "https://wiki.example.test/" + string(documentID)}, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := app.EncodeSourceRef(app.SourceRefIdentity{Source: accepted.Source, Revision: accepted.Version.Version})
	if err != nil {
		t.Fatal(err)
	}
	return ref
}
func writeRootCatalogTargets(t *testing.T, workspace *hostMaintenanceContent, targets ...string) {
	t.Helper()
	path := filepath.Join(workspace.Root(), testIndexPath)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		content = append(content, []byte("\n* [Target]("+target+")\n")...)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
func mustReadHierarchyFile(t *testing.T, workspace *hostMaintenanceContent, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(workspace.Root(), path))
	if err != nil {
		t.Fatal(err)
	}
	return content
}
func writeMaintenanceFixture(t *testing.T, workspace *hostMaintenanceContent, path string, content []byte) {
	t.Helper()
	target := filepath.Join(workspace.Root(), path)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
func newHierarchyWorkspace(t *testing.T) *hostMaintenanceContent {
	t.Helper()
	workspace, err := newMaintenanceTestContent(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	ref := acceptStructuredWorkspaceSource(t, workspace, "legacy", "old.md", "v1")
	writeMaintenanceFixture(t, workspace, testPageOnePath, validWorkspacePage(testPageOneID, "One", ref, ""))
	writeMaintenanceFixture(t, workspace, "wiki/entities/two.md", validWorkspacePage("entities/two", "Two", ref, ""))
	writeRootCatalogTargets(t, workspace, "entities/one.md", "entities/two.md")
	writeMaintenanceFixture(t, workspace, testHierarchyOldPath, []byte("# Old\n\n* [One](../../entities/one.md)\n"))
	return workspace
}
func hierarchyStagePlan(t *testing.T, workspace *hostMaintenanceContent) knowl.ValidatedHierarchyPlan {
	t.Helper()
	schema, err := workspace.Schema(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := workspace.HierarchySnapshotDigest(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	return knowl.ValidatedHierarchyPlan{Scope: testScope, SchemaDigest: schema.Digest, SnapshotDigest: snapshot, Mutations: []knowl.HierarchyMutation{
		{Action: knowl.SourceMutationWrite, Path: testIndexPath, ExpectedDigest: digestBytes(mustReadHierarchyFile(t, workspace, testIndexPath)), Content: []byte(rootIndexContent + "\n* [Architecture](catalogs/architecture/index.md)\n* [Product](catalogs/product/index.md)\n")},
		{Action: knowl.SourceMutationWrite, Path: testHierarchyArchPath, Content: []byte("# Architecture\n\n* [One](../../entities/one.md)\n")},
		{Action: knowl.SourceMutationDelete, Path: testHierarchyOldPath, ExpectedDigest: digestBytes(mustReadHierarchyFile(t, workspace, testHierarchyOldPath))},
		{Action: knowl.SourceMutationWrite, Path: testHierarchyProdPath, Content: []byte("# Product\n\n* [Two](../../entities/two.md)\n")},
	}}
}
func retirementDeleteFixture(t *testing.T) (*hostMaintenanceContent, knowl.ValidatedEditPlan, maintenanceLifecycleReader) {
	t.Helper()
	workspace, err := newMaintenanceTestContent(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	ref := acceptStructuredWorkspaceSource(t, workspace, "legacy", "old.md", "v1")
	page := validWorkspacePage(testPageOneID, "One", ref, "old facts")
	writeMaintenanceFixture(t, workspace, testPageOnePath, page)
	writeRootCatalogTargets(t, workspace, "entities/one.md")
	schema, err := workspace.Schema(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 6, 0, 0, 0, time.UTC)
	authority := knowl.SourceLifecycleAuthority{Scope: testScope, Generation: strings.Repeat("a", 64), Epoch: 7, UpdatedAt: now}
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: "legacy"}
	state := knowl.SourceLifecycle{Scope: testScope, Owner: owner, State: knowl.SourceLifecycleRetiring, ConfigDigest: strings.Repeat("b", 64), AuthorityGeneration: authority.Generation, AuthorityEpoch: 7, ActivatedGeneration: strings.Repeat("c", 64), ActivatedEpoch: 2, RetirementGeneration: strings.Repeat("d", 64), RetirementEpoch: 7, ActivatedAt: now.Add(-2 * time.Hour), RetiringAt: now.Add(-time.Hour), UpdatedAt: now}
	retirement := &knowl.RetirementPlanContext{Scope: testScope, PageID: testPageOneID, PagePath: testPageOnePath, PageDigest: digestBytes(page), AuthorityGeneration: authority.Generation, AuthorityEpoch: 7, Owners: []knowl.RetirementOwnerAuthority{{Owner: owner, ConfigDigest: state.ConfigDigest, RetirementGeneration: state.RetirementGeneration, RetirementEpoch: 7}}}
	plan := knowl.ValidatedEditPlan{OperationID: "retire-page", Scope: testScope, SchemaDigest: schema.Digest, SourceRefs: []string{}, Retirement: retirement, Edits: []knowl.FileEdit{{Action: knowl.SourceMutationDelete, Path: testPageOnePath, ExpectedDigest: digestBytes(page)}, {Action: knowl.SourceMutationWrite, Path: testIndexPath, ExpectedDigest: digestBytes(mustReadHierarchyFile(t, workspace, testIndexPath)), Content: []byte(rootIndexContent)}}}
	return workspace, plan, maintenanceLifecycleReader{authority: authority, states: map[knowl.SourceOwner]knowl.SourceLifecycle{owner: state}}
}
func commitRetirementForTest(ctx context.Context, coordinator app.ApplyCoordinator, lifecycles app.SourceLifecycleReader, workspace *hostMaintenanceContent, staged knowl.StagedChange) (knowl.ContentCommit, error) {
	committer, err := newHostRetirementCommitter(coordinator, lifecycles, workspace.Workspace, workspace)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	return committer.RunRetirementApply(ctx, staged, func(context.Context) (bool, error) { return true, nil }, func(context.Context, knowl.ContentCommit) error { return nil })
}
