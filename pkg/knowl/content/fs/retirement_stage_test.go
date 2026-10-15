package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/internal/retirementcommit"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type retirementLifecycleReader struct {
	authority knowl.SourceLifecycleAuthority
	states    map[knowl.SourceOwner]knowl.SourceLifecycle
}

func (reader retirementLifecycleReader) SourceLifecycleAuthority(ctx context.Context, scope knowl.ScopeRef) (knowl.SourceLifecycleAuthority, error) {
	if err := ctx.Err(); err != nil {
		return knowl.SourceLifecycleAuthority{}, err
	}
	if scope != reader.authority.Scope {
		return knowl.SourceLifecycleAuthority{}, app.ErrSourceLifecycleUnowned
	}
	return reader.authority, nil
}

func (reader retirementLifecycleReader) SourceLifecycle(ctx context.Context, scope knowl.ScopeRef, owner knowl.SourceOwner) (knowl.SourceLifecycle, error) {
	if err := ctx.Err(); err != nil {
		return knowl.SourceLifecycle{}, err
	}
	state, ok := reader.states[owner]
	if scope != reader.authority.Scope || !ok {
		return knowl.SourceLifecycle{}, app.ErrSourceLifecycleUnowned
	}
	return state, nil
}

func TestWorkspaceRetirementDeleteRequiresFreshAuthorizedCommitBoundary(t *testing.T) {
	workspace, plan, lifecycle := retirementDeleteFixture(t)
	staged, err := workspace.StagePlan(t.Context(), plan)
	if err != nil {
		t.Fatalf("StagePlan() error = %v", err)
	}
	if !reflect.DeepEqual(staged.Retirement, plan.Retirement) || !slices.Equal(staged.Files, []string{testPageOnePath, testIndexPath}) {
		t.Fatalf("staged retirement = %#v", staged)
	}
	if _, err := workspace.Commit(t.Context(), staged); !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("ordinary Commit() error = %v, want plan conflict", err)
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), filepath.FromSlash(testPageOnePath))); err != nil {
		t.Fatalf("ordinary commit changed page: %v", err)
	}

	commit, err := commitRetirementForTest(t.Context(), newRetirementFSCoordinator(), lifecycle, workspace, staged)
	if err != nil {
		t.Fatalf("retirement commit error = %v", err)
	}
	if !slices.Equal(commit.Files, []string{testPageOnePath, testIndexPath, canonicalLogPath}) {
		t.Fatalf("committed files = %#v", commit.Files)
	}
	if _, err := os.Stat(filepath.Join(workspace.Root(), filepath.FromSlash(testPageOnePath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted page stat = %v, want missing", err)
	}
	root, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(testIndexPath)))
	if err != nil || string(root) != rootIndexContent {
		t.Fatalf("root after retirement = %q, error=%v", root, err)
	}
}

func TestWorkspaceRetirementPublishesPagesGraduallyWhileOwnerRemainsRetiring(t *testing.T) {
	workspace, firstPlan, lifecycle := retirementDeleteFixture(t)
	oldRef := retirementPageSourceRef(t, workspace)
	activeRef := acceptStructuredWorkspaceSource(t, workspace, "replacement", "current.md", "v1")
	secondID := knowl.PageID("entities/two")
	secondPath := testHierarchyPageTwo
	secondBefore := validWorkspacePageRefs(string(secondID), "Two", []string{oldRef, activeRef}, "mixed facts")
	writeCanonicalFixture(t, workspace, secondPath, secondBefore)
	writeRootCatalogTargets(t, workspace, "entities/two.md")

	rootPath := filepath.Join(workspace.Root(), filepath.FromSlash(testIndexPath))
	rootBefore, err := os.ReadFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	rootAfterFirst := []byte(rootIndexContent + "\n* [Target](entities/two.md)\n")
	firstPlan.Edits[1].ExpectedDigest = digestBytes(rootBefore)
	firstPlan.Edits[1].Content = rootAfterFirst
	firstStaged, err := workspace.StagePlan(t.Context(), firstPlan)
	if err != nil {
		t.Fatalf("stage first page: %v", err)
	}
	if _, err := commitRetirementForTest(t.Context(), newRetirementFSCoordinator(), &lifecycle, workspace, firstStaged); err != nil {
		t.Fatalf("commit first page: %v", err)
	}

	if _, err := os.Stat(filepath.Join(workspace.Root(), filepath.FromSlash(testPageOnePath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old-only page stat = %v, want absent", err)
	}
	unchangedSecond, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(secondPath)))
	if err != nil || !slices.Equal(unchangedSecond, secondBefore) {
		t.Fatalf("unreconciled page changed: equal=%t error=%v", slices.Equal(unchangedSecond, secondBefore), err)
	}
	owner := firstPlan.Retirement.Owners[0].Owner
	if state := lifecycle.states[owner]; state.State != knowl.SourceLifecycleRetiring {
		t.Fatalf("owner state after partial progress = %q, want retiring", state.State)
	}
	remaining, err := workspace.PageProvenance(t.Context(), testScope, owner, "", app.CanonicalProvenanceLimits{})
	if err != nil || len(remaining.Items) != 1 || remaining.Items[0].ID != secondID || !remaining.EOF || !remaining.Authoritative {
		t.Fatalf("remaining retiring provenance = %#v, %v", remaining, err)
	}

	secondAfter := validWorkspacePage(string(secondID), "Two", activeRef, "active facts")
	secondPlan := knowl.ValidatedEditPlan{
		OperationID:  "retire-page-two",
		Scope:        testScope,
		SchemaDigest: firstPlan.SchemaDigest,
		SourceRefs:   []string{activeRef},
		Retirement: &knowl.RetirementPlanContext{
			Scope: testScope, PageID: secondID, PagePath: secondPath, PageDigest: digestBytes(secondBefore),
			AuthorityGeneration: firstPlan.Retirement.AuthorityGeneration,
			AuthorityEpoch:      firstPlan.Retirement.AuthorityEpoch,
			Owners:              slices.Clone(firstPlan.Retirement.Owners),
		},
		Edits: []knowl.FileEdit{{
			Action: knowl.SourceMutationWrite, Path: secondPath,
			ExpectedDigest: digestBytes(secondBefore), Content: secondAfter,
		}},
	}
	secondStaged, err := workspace.StagePlan(t.Context(), secondPlan)
	if err != nil {
		t.Fatalf("stage second page: %v; diagnostics=%#v", err, secondStaged.Diagnostics)
	}
	if _, err := commitRetirementForTest(t.Context(), newRetirementFSCoordinator(), &lifecycle, workspace, secondStaged); err != nil {
		t.Fatalf("commit second page: %v", err)
	}
	finalSecond, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(secondPath)))
	if err != nil || !slices.Equal(finalSecond, secondAfter) {
		t.Fatalf("recomputed page = %q, error=%v", finalSecond, err)
	}
	zero, err := workspace.PageProvenance(t.Context(), testScope, owner, "", app.CanonicalProvenanceLimits{})
	if err != nil || len(zero.Items) != 0 || !zero.EOF || !zero.Authoritative {
		t.Fatalf("zero retiring provenance = %#v, %v", zero, err)
	}
}

func TestWorkspaceOrdinaryPlanCannotDeleteOrRelaxLineage(t *testing.T) {
	workspace, plan, _ := retirementDeleteFixture(t)
	plan.OperationID = "ordinary-delete"
	plan.Retirement = nil
	if staged, err := workspace.StagePlan(t.Context(), plan); !errors.Is(err, ErrPlanConflict) || staged.Digest != "" {
		t.Fatalf("ordinary delete stage = %#v, error=%v", staged, err)
	}

	oldRef := acceptStructuredWorkspaceSource(t, workspace, "other-old", "old.md", "v1")
	newRef := acceptStructuredWorkspaceSource(t, workspace, "other-new", "new.md", "v1")
	path := "wiki/entities/lineage.md"
	before := validWorkspacePage("entities/lineage", "Lineage", oldRef, "old")
	if err := os.WriteFile(filepath.Join(workspace.Root(), filepath.FromSlash(path)), before, 0o600); err != nil {
		t.Fatal(err)
	}
	writeRootCatalogTargets(t, workspace, "entities/lineage.md")
	schema, err := workspace.Schema(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	_, err = workspace.StagePlan(t.Context(), knowl.ValidatedEditPlan{
		OperationID: "ordinary-lineage", Scope: testScope, SchemaDigest: schema.Digest, SourceRefs: []string{newRef},
		Edits: []knowl.FileEdit{{Path: path, ExpectedDigest: digestBytes(before), Content: validWorkspacePage("entities/lineage", "Lineage", newRef, "new")}},
	})
	if !errors.Is(err, ErrContentInvalid) {
		t.Fatalf("ordinary lineage removal error = %v, want content invalid", err)
	}
}

func TestWorkspaceRetirementSafeSubsetNeverPublishesCatalogWithoutPage(t *testing.T) {
	workspace, plan, _ := retirementDeleteFixture(t)
	plan.OperationID = "retirement-all-or-none"
	plan.SourceRefs = []string{testMissingSourceRef}
	plan.Edits[0] = knowl.FileEdit{
		Action: knowl.SourceMutationWrite, Path: testPageOnePath, ExpectedDigest: plan.Retirement.PageDigest,
		Content: validWorkspacePage(testPageOneID, "One", testMissingSourceRef, "unsupported"),
	}
	staged, err := workspace.StagePlan(t.Context(), plan)
	var invalid *contentValidationError
	if !errors.Is(err, ErrContentInvalid) || !errors.As(err, &invalid) || invalid.rule != "plan.empty_safe_subset" {
		t.Fatalf("StagePlan() error = %v, want content invalid", err)
	}
	want := []knowl.MaintenanceDiagnostic{
		{Code: knowl.DiagnosticCitationUnknownSource, Path: testPageOnePath},
		{Code: knowl.DiagnosticCatalogDependencyReject, Path: testIndexPath, Target: "entities/one.md"},
	}
	if !reflect.DeepEqual(staged.Diagnostics, want) || staged.Digest != "" || len(staged.Files) != 0 {
		t.Fatalf("rejected retirement stage = %#v, error=%v, want diagnostics %#v", staged, err, want)
	}
	root, readErr := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(testIndexPath)))
	if readErr != nil || string(root) == rootIndexContent {
		t.Fatalf("canonical root changed during staging or fixture invalid: %q, error=%v", root, readErr)
	}
}

func TestWorkspaceRetirementRejectsRetainedRetiringRefWithExactOwnerDiagnostic(t *testing.T) {
	workspace, plan, _ := retirementDeleteFixture(t)
	pages, err := workspace.ReadPages(t.Context(), testScope, []knowl.PageID{testPageOneID}, knowl.ReadLimits{Pages: 1, Bytes: 1 << 20, Characters: 1 << 20, Depth: 1})
	if err != nil || len(pages) != 1 || len(pages[0].SourceRefs) != 1 {
		t.Fatalf("ReadPages() = %#v, error=%v", pages, err)
	}
	oldRef := pages[0].SourceRefs[0]
	plan.OperationID = "retirement-retained-ref"
	plan.SourceRefs = []string{oldRef}
	plan.Edits = []knowl.FileEdit{{
		Action: knowl.SourceMutationWrite, Path: testPageOnePath, ExpectedDigest: plan.Retirement.PageDigest,
		Content: validWorkspacePage(testPageOneID, "One", oldRef, "still retired"),
	}}
	staged, err := workspace.StagePlan(t.Context(), plan)
	if !errors.Is(err, ErrContentInvalid) {
		t.Fatalf("StagePlan() error = %v, want content invalid", err)
	}
	want := []knowl.MaintenanceDiagnostic{{Code: app.CitationSourceRetiringCode, Path: testPageOnePath, Target: "wiki-filesystem:legacy"}}
	if !reflect.DeepEqual(staged.Diagnostics, want) || staged.Digest != "" {
		t.Fatalf("retiring ref rejection = %#v, want %#v", staged, want)
	}
}

func TestWorkspaceRetirementCannotRemoveUnownedActiveLineage(t *testing.T) {
	workspace, plan, _ := retirementDeleteFixture(t)
	activeRef := acceptStructuredWorkspaceSource(t, workspace, "active", "current.md", "v1")
	pagePath := filepath.Join(workspace.Root(), filepath.FromSlash(testPageOnePath))
	before := validWorkspacePageRefs(testPageOneID, "One", []string{retirementPageSourceRef(t, workspace), activeRef}, "mixed")
	if err := os.WriteFile(pagePath, before, 0o600); err != nil {
		t.Fatal(err)
	}
	plan.OperationID = "retirement-unowned-lineage"
	plan.Retirement.PageDigest = digestBytes(before)
	plan.Edits = []knowl.FileEdit{{Action: knowl.SourceMutationDelete, Path: testPageOnePath, ExpectedDigest: digestBytes(before)}}
	staged, err := workspace.StagePlan(t.Context(), plan)
	if !errors.Is(err, ErrContentInvalid) {
		t.Fatalf("StagePlan() error = %v, want content invalid", err)
	}
	want := []knowl.MaintenanceDiagnostic{{Code: "citation.lineage_removed", Path: testPageOnePath}}
	if !reflect.DeepEqual(staged.Diagnostics, want) {
		t.Fatalf("lineage rejection = %#v, want %#v", staged.Diagnostics, want)
	}
}

func TestWorkspaceRetirementReplacementRejectsMissingUnknownUndeclaredAndRetiringRefs(t *testing.T) {
	tests := []struct {
		name       string
		contentRef func(t *testing.T, workspace *Workspace) string
		planRefs   func(string) []string
		want       knowl.MaintenanceDiagnostic
	}{
		{
			name: "missing", contentRef: func(*testing.T, *Workspace) string { return "" },
			planRefs: func(string) []string { return nil },
			want:     knowl.MaintenanceDiagnostic{Code: "citation.missing", Path: testPageOnePath},
		},
		{
			name: "unknown", contentRef: func(*testing.T, *Workspace) string { return testMissingSourceRef },
			planRefs: func(ref string) []string { return []string{ref} },
			want:     knowl.MaintenanceDiagnostic{Code: knowl.DiagnosticCitationUnknownSource, Path: testPageOnePath},
		},
		{
			name: "undeclared", contentRef: func(t *testing.T, workspace *Workspace) string {
				return acceptStructuredWorkspaceSource(t, workspace, "active", "current.md", "v1")
			},
			planRefs: func(string) []string { return nil },
			want:     knowl.MaintenanceDiagnostic{Code: app.CitationPlanMissingCode, Path: testPageOnePath},
		},
		{
			name: "retiring", contentRef: retirementPageSourceRef,
			planRefs: func(ref string) []string { return []string{ref} },
			want:     knowl.MaintenanceDiagnostic{Code: app.CitationSourceRetiringCode, Path: testPageOnePath, Target: "wiki-filesystem:legacy"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, plan, _ := retirementDeleteFixture(t)
			ref := test.contentRef(t, workspace)
			refs := []string{}
			if ref != "" {
				refs = []string{ref}
			}
			content := validWorkspacePageRefs(testPageOneID, "One", refs, "replacement")
			if len(refs) == 0 {
				content = []byte("---\nid: entities/one\ntitle: One\ntype: entity\nsource_refs: []\n---\n# One\n\nreplacement\n")
			}
			plan.OperationID = "retirement-ref-" + test.name
			plan.SourceRefs = test.planRefs(ref)
			plan.Edits = []knowl.FileEdit{{
				Action: knowl.SourceMutationWrite, Path: testPageOnePath, ExpectedDigest: plan.Retirement.PageDigest,
				Content: content,
			}}
			staged, err := workspace.StagePlan(t.Context(), plan)
			if !errors.Is(err, ErrContentInvalid) {
				t.Fatalf("StagePlan() error = %v, want content invalid", err)
			}
			if !reflect.DeepEqual(staged.Diagnostics, []knowl.MaintenanceDiagnostic{test.want}) || staged.Digest != "" {
				t.Fatalf("rejected stage = %#v, want %#v", staged, test.want)
			}
		})
	}
}

func TestWorkspaceRetirementCommitRevalidatesCatalogPreimageAtomically(t *testing.T) {
	workspace, plan, lifecycle := retirementDeleteFixture(t)
	staged, err := workspace.StagePlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(workspace.Root(), filepath.FromSlash(testIndexPath))
	if err := os.WriteFile(rootPath, []byte(rootIndexContent+"\n<!-- concurrent -->\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := commitRetirementForTest(t.Context(), newRetirementFSCoordinator(), lifecycle, workspace, staged); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("retirement commit error = %v, want precondition", err)
	}
	page, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(testPageOnePath)))
	if err != nil || digestBytes(page) != plan.Retirement.PageDigest {
		t.Fatalf("page changed after stale catalog = %q, error=%v", page, err)
	}
}

func TestWorkspaceRetirementCommitRevalidatesPagePreimageAtomically(t *testing.T) {
	workspace, plan, lifecycle := retirementDeleteFixture(t)
	staged, err := workspace.StagePlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	pagePath := filepath.Join(workspace.Root(), filepath.FromSlash(testPageOnePath))
	changed := validWorkspacePage(testPageOneID, "One", retirementPageSourceRef(t, workspace), "concurrent change")
	if err := os.WriteFile(pagePath, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(workspace.Root(), filepath.FromSlash(testIndexPath))
	rootBefore, err := os.ReadFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commitRetirementForTest(t.Context(), newRetirementFSCoordinator(), lifecycle, workspace, staged); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("retirement commit error = %v, want precondition", err)
	}
	page, pageErr := os.ReadFile(pagePath)
	root, rootErr := os.ReadFile(rootPath)
	if pageErr != nil || rootErr != nil || !slices.Equal(page, changed) || !slices.Equal(root, rootBefore) {
		t.Fatalf("stale page commit was not atomic: pageErr=%v rootErr=%v pageSame=%v rootSame=%v", pageErr, rootErr, slices.Equal(page, changed), slices.Equal(root, rootBefore))
	}
}

func TestWorkspaceRetirementCommitRejectsLifecycleDriftBeforeWriter(t *testing.T) {
	workspace, plan, lifecycle := retirementDeleteFixture(t)
	staged, err := workspace.StagePlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	owner := plan.Retirement.Owners[0].Owner
	state := lifecycle.states[owner]
	state.ConfigDigest = strings.Repeat("e", 64)
	lifecycle.states[owner] = state

	if _, err := commitRetirementForTest(t.Context(), newRetirementFSCoordinator(), &lifecycle, workspace, staged); !errors.Is(err, app.ErrRetirementLifecycleConflict) {
		t.Fatalf("retirement commit error = %v, want lifecycle conflict", err)
	}
	page, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(testPageOnePath)))
	if err != nil || digestBytes(page) != plan.Retirement.PageDigest {
		t.Fatalf("page changed after lifecycle drift = %q, error=%v", page, err)
	}
}

func TestWorkspaceRecoversRetirementUnitAtEveryJournalBoundary(t *testing.T) {
	tests := []struct {
		name       string
		point      string
		index      int
		wantAfter  bool
		wantAction string
	}{
		{name: testFaultBeforeJournalName, point: testFaultBeforeJournalPoint, index: -1},
		{name: testFaultAfterPreimagesName, point: testFaultAfterPreimagesPoint, index: -1},
		{name: recoveryPrepared, point: recoveryPrepared, index: -1, wantAction: recoveryRolledBack},
		{name: "after page delete", point: commitFaultApplied, index: 0, wantAction: recoveryRolledBack},
		{name: "after catalog write", point: commitFaultApplied, index: 1, wantAction: recoveryRolledBack},
		{name: "after log write", point: commitFaultApplied, index: 2, wantAction: recoveryRolledBack},
		{name: testDurablyCommitted, point: recoveryCommitted, index: -1, wantAfter: true, wantAction: recoveryCompleted},
		{name: "after receipt write", point: commitFaultReceipt, index: -1, wantAfter: true, wantAction: recoveryCompleted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, plan, lifecycle := retirementDeleteFixture(t)
			pagePath := filepath.Join(workspace.Root(), filepath.FromSlash(testPageOnePath))
			rootPath := filepath.Join(workspace.Root(), filepath.FromSlash(testIndexPath))
			logPath := filepath.Join(workspace.Root(), filepath.FromSlash(canonicalLogPath))
			beforePage, err := os.ReadFile(pagePath)
			if err != nil {
				t.Fatal(err)
			}
			beforeRoot, err := os.ReadFile(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			beforeLog, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			staged, err := workspace.StagePlan(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			workspace.commitFault = func(point string, index int) error {
				if point == test.point && index == test.index {
					return errInjectedCommitFault
				}
				return nil
			}
			if _, err := commitRetirementForTest(t.Context(), newRetirementFSCoordinator(), lifecycle, workspace, staged); !errors.Is(err, errInjectedCommitFault) {
				t.Fatalf("retirement commit error = %v, want injected fault", err)
			}

			reopened, err := New(workspace.Root())
			if err != nil {
				t.Fatal(err)
			}
			results, err := reopened.Recover(t.Context())
			if err != nil {
				t.Fatalf("Recover() error = %v", err)
			}
			if test.wantAction == "" {
				if len(results) != 0 {
					t.Fatalf("recovery results = %#v, want none", results)
				}
			} else if len(results) != 1 || results[0].Action != test.wantAction || results[0].OperationID != plan.OperationID {
				t.Fatalf("recovery results = %#v, want %q", results, test.wantAction)
			}
			receipt, committed, receiptErr := reopened.RetirementCommitReceipt(t.Context(), staged)
			if receiptErr != nil {
				t.Fatalf("RetirementCommitReceipt() error = %v", receiptErr)
			}
			if committed != test.wantAfter {
				t.Fatalf("RetirementCommitReceipt() committed = %v, want %v; receipt=%#v", committed, test.wantAfter, receipt)
			}
			wantFiles := append(slices.Clone(staged.Files), canonicalLogPath)
			if committed && (receipt.OperationID != staged.OperationID || receipt.Generation != staged.Digest || !slices.Equal(receipt.Files, wantFiles)) {
				t.Fatalf("RetirementCommitReceipt() = %#v, want staged identity", receipt)
			}
			page, pageErr := os.ReadFile(pagePath)
			root, rootErr := os.ReadFile(rootPath)
			log, logErr := os.ReadFile(logPath)
			if rootErr != nil || logErr != nil {
				t.Fatalf("read recovered catalog/log: root=%v log=%v", rootErr, logErr)
			}
			if test.wantAfter {
				if !errors.Is(pageErr, os.ErrNotExist) || string(root) != rootIndexContent || slices.Equal(log, beforeLog) {
					t.Fatalf("completed state: pageErr=%v root=%q logChanged=%v", pageErr, root, !slices.Equal(log, beforeLog))
				}
			} else if pageErr != nil || !slices.Equal(page, beforePage) || !slices.Equal(root, beforeRoot) || !slices.Equal(log, beforeLog) {
				t.Fatalf("rolled-back state: pageErr=%v pageSame=%v rootSame=%v logSame=%v", pageErr, slices.Equal(page, beforePage), slices.Equal(root, beforeRoot), slices.Equal(log, beforeLog))
			}
			assertDirectoryEmpty(t, filepath.Join(workspace.Root(), knowlDir, "recovery"))
		})
	}
}

func TestValidateRetirementRecoveryJournalPreservesCommitTargetOrder(t *testing.T) {
	const operationID = "retire-late-page"
	pagePath := "wiki/z-last.md"
	valid := recoveryJournal{
		OperationID: operationID,
		Writer:      stageWriterRetirement,
		Scope:       string(testScope),
		State:       recoveryCommitted,
		Generation:  strings.Repeat("a", 64),
		Files:       []string{pagePath, canonicalLogPath},
		Entries: []recoveryEntry{
			{Action: knowl.SourceMutationDelete, Target: pagePath, HadOld: true, Backup: "page.old"},
			{Action: knowl.SourceMutationWrite, Target: canonicalLogPath, HadOld: true, Backup: "log.old", Digest: strings.Repeat("b", 64)},
		},
	}

	key := retirementRecoveryKey(string(testScope), operationID)
	if _, err := validateRecoveryJournal(token(key)+".yaml", valid); err != nil {
		t.Fatalf("validateRecoveryJournal() error = %v", err)
	}

	reordered := valid
	reordered.Files = []string{canonicalLogPath, pagePath}
	if _, err := validateRecoveryJournal(token(key)+".yaml", reordered); !errors.Is(err, ErrWorkspaceInvalid) {
		t.Fatalf("reordered receipt files error = %v, want workspace invalid", err)
	}

	missingLog := valid
	missingLog.Entries = slices.Clone(valid.Entries[:1])
	missingLog.Files = slices.Clone(valid.Files[:1])
	if _, err := validateRecoveryJournal(token(key)+".yaml", missingLog); !errors.Is(err, ErrWorkspaceInvalid) {
		t.Fatalf("missing canonical log error = %v, want workspace invalid", err)
	}
}

func TestWorkspaceRetirementCommitCancellationReleasesApplyFenceWithoutPublishing(t *testing.T) {
	workspace, plan, lifecycle := retirementDeleteFixture(t)
	staged, err := workspace.StagePlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := workspace.lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	writer, err := New(workspace.Root())
	if err != nil {
		unlock()
		t.Fatal(err)
	}
	coordinator := newRetirementFSCoordinator()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	_, commitErr := commitRetirementForTest(ctx, coordinator, lifecycle, writer, staged)
	unlock()
	if !errors.Is(commitErr, context.DeadlineExceeded) || !errors.Is(commitErr, ErrWorkspaceBusy) {
		t.Fatalf("retirement commit error = %v, want deadline and workspace busy", commitErr)
	}
	release, err := coordinator.Acquire(t.Context())
	if err != nil {
		t.Fatalf("apply fence remained held: %v", err)
	}
	release()
	page, pageErr := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(testPageOnePath)))
	if pageErr != nil || digestBytes(page) != plan.Retirement.PageDigest {
		t.Fatalf("canceled commit changed page: digest=%q error=%v", digestBytes(page), pageErr)
	}
}

type retirementFSCoordinator struct {
	gate chan struct{}
}

func commitRetirementForTest(ctx context.Context, coordinator app.ApplyCoordinator, lifecycles app.SourceLifecycleReader, workspace *Workspace, staged knowl.StagedChange) (knowl.ContentCommit, error) {
	release, err := coordinator.Acquire(ctx)
	if err != nil {
		return knowl.ContentCommit{}, err
	}
	defer release()
	if err := app.RevalidateRetirementStage(ctx, lifecycles, staged); err != nil {
		return knowl.ContentCommit{}, err
	}
	return workspace.CommitRetirementBound(ctx, retirementcommit.NewRequest(staged))
}

func newRetirementFSCoordinator() *retirementFSCoordinator {
	return &retirementFSCoordinator{gate: make(chan struct{}, 1)}
}

func (coordinator *retirementFSCoordinator) Acquire(ctx context.Context) (func(), error) {
	select {
	case coordinator.gate <- struct{}{}:
		return func() { <-coordinator.gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func retirementDeleteFixture(t *testing.T) (*Workspace, knowl.ValidatedEditPlan, retirementLifecycleReader) {
	t.Helper()
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	oldRef := acceptStructuredWorkspaceSource(t, workspace, "legacy", "old.md", "v1")
	page := validWorkspacePage(testPageOneID, "One", oldRef, "old facts")
	pagePath := filepath.Join(workspace.Root(), filepath.FromSlash(testPageOnePath))
	if err := os.MkdirAll(filepath.Dir(pagePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pagePath, page, 0o600); err != nil {
		t.Fatal(err)
	}
	writeRootCatalogTargets(t, workspace, "entities/one.md")
	root, err := os.ReadFile(filepath.Join(workspace.Root(), filepath.FromSlash(testIndexPath)))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := workspace.Schema(t.Context(), testScope)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 6, 0, 0, 0, time.UTC)
	authority := knowl.SourceLifecycleAuthority{Scope: testScope, Generation: strings.Repeat("a", 64), Epoch: 7, UpdatedAt: now}
	owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterFilesystem, SourceID: "legacy"}
	state := knowl.SourceLifecycle{
		Scope: testScope, Owner: owner, State: knowl.SourceLifecycleRetiring, ConfigDigest: strings.Repeat("b", 64),
		AuthorityGeneration: authority.Generation, AuthorityEpoch: authority.Epoch,
		ActivatedGeneration: strings.Repeat("c", 64), ActivatedEpoch: 2,
		RetirementGeneration: strings.Repeat("d", 64), RetirementEpoch: 7,
		ActivatedAt: now.Add(-2 * time.Hour), RetiringAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	retirement := &knowl.RetirementPlanContext{
		Scope: testScope, PageID: testPageOneID, PagePath: testPageOnePath, PageDigest: digestBytes(page),
		AuthorityGeneration: authority.Generation, AuthorityEpoch: authority.Epoch,
		Owners: []knowl.RetirementOwnerAuthority{{Owner: owner, ConfigDigest: state.ConfigDigest, RetirementGeneration: state.RetirementGeneration, RetirementEpoch: state.RetirementEpoch}},
	}
	plan := knowl.ValidatedEditPlan{
		OperationID: "retire-page", Scope: testScope, SchemaDigest: schema.Digest, SourceRefs: []string{}, Retirement: retirement,
		Edits: []knowl.FileEdit{
			{Action: knowl.SourceMutationDelete, Path: testPageOnePath, ExpectedDigest: digestBytes(page)},
			{Action: knowl.SourceMutationWrite, Path: testIndexPath, ExpectedDigest: digestBytes(root), Content: []byte(rootIndexContent)},
		},
	}
	return workspace, plan, retirementLifecycleReader{authority: authority, states: map[knowl.SourceOwner]knowl.SourceLifecycle{owner: state}}
}

func retirementPageSourceRef(t *testing.T, workspace *Workspace) string {
	t.Helper()
	pages, err := workspace.ReadPages(t.Context(), testScope, []knowl.PageID{testPageOneID}, knowl.ReadLimits{Pages: 1, Bytes: 1 << 20, Characters: 1 << 20, Depth: 1})
	if err != nil || len(pages) != 1 || len(pages[0].SourceRefs) == 0 {
		t.Fatalf("ReadPages() = %#v, error=%v", pages, err)
	}
	return pages[0].SourceRefs[0]
}
