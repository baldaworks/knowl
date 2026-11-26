package knowl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

const incidentPageID domain.PageID = "concepts/runtime-contribution-catalog"
const incidentPagePath = "wiki/concepts/runtime-contribution-catalog.md"
const incidentRevision = "220fccbd15b7f9dfc03aaf66871aed418c5d42d691afd1c16e5ddcdd01cd304c"
const incidentSourceID domain.SourceID = "balda-docs"
const incidentRetainedFacts = "Current Git runtime fact. Accepted application fact."

// Synthetic facts reproduce the original identities without copying incident
// content. The only canonical cleanup runs through normal retirement publication.
func TestLegacyFilesystemIncidentAutomaticallyRetiresAndRetriesThreeDocuments(t *testing.T) {
	ctx := t.Context()
	content, err := newMaintenanceTestContent(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = content.Init(); err != nil {
		t.Fatal(err)
	}
	old := incidentAccept(t, content, "wiki-filesystem", "docs/architecture/index.md", incidentRevision, "Obsolete filesystem fact.")
	oldRef := app.SourceRefKey(old)
	if oldRef != "wiki-filesystem:balda-docs/docs/architecture/index.md@"+incidentRevision {
		t.Fatal(oldRef)
	}
	docs := []domain.AcceptedSource{
		incidentAccept(t, content, "git", "docs/architecture/runtime-contribution-catalog.md", "current-1", "Current Git runtime fact."),
		incidentAccept(t, content, "git", "docs/reference/plugins.md", "current-2", "Current Git plugin fact."),
		incidentAccept(t, content, "git", "docs/reference/skills.md", "current-3", "Current Git skill fact."),
	}
	inline := incidentAccept(t, content, "inline", "application-note", "v1", "Accepted application fact.")
	refs := []string{oldRef, app.SourceRefKey(docs[0]), app.SourceRefKey(inline)}
	slices.Sort(refs)
	writeRootCatalogTargets(t, content, "concepts/runtime-contribution-catalog.md", "concepts/obsolete-filesystem.md")
	schema, err := content.Schema(ctx, testScope)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := domain.ValidatedEditPlan{OperationID: "incident-before-lifecycle", Scope: testScope, SchemaDigest: schema.Digest, RequiredSourceRef: oldRef, SourceRefs: refs, Edits: []domain.FileEdit{
		{Path: incidentPagePath, Content: incidentMarkdown(incidentPageID, refs, "Obsolete filesystem fact. "+incidentRetainedFacts)},
		{Path: "wiki/concepts/obsolete-filesystem.md", Content: incidentMarkdown("concepts/obsolete-filesystem", []string{oldRef}, "Obsolete filesystem fact.")},
	}}
	stage, err := content.Workspace.StagePlan(ctx, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	initialCommit, err := content.Workspace.Commit(ctx, stage)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "incident.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	initialCommit.Snapshot, err = content.Snapshot(ctx, testScope)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Project(ctx, initialCommit); err != nil {
		t.Fatal(err)
	}
	oldKey := domain.OperationKey{Scope: testScope, Source: old.Source, Version: old.Version}
	history, err := store.Reserve(ctx, oldKey, domain.OperationMeta{Key: oldKey, AcceptedSource: old, Schema: schema, SchemaDigest: schema.Digest})
	if err != nil {
		t.Fatal(err)
	}
	originalOperation, err := store.Operation(ctx, testScope, history.ID)
	if err != nil {
		t.Fatal(err)
	}
	originalDescriptor, err := store.Execution(ctx, testScope, history.ID)
	if err != nil {
		t.Fatal(err)
	}
	rawBefore := mustReadHierarchyFile(t, content, old.ManifestRef)
	oldBytes, err := content.ReadSource(ctx, old, app.DefaultReadLimits())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	owner := domain.SourceOwner{Adapter: domain.SourceAdapterGit, SourceID: incidentSourceID}
	desired := []app.DesiredSourceOwner{{Owner: owner, ConfigDigest: strings.Repeat("a", 64)}}
	if err = store.ReconcileSourceLifecycles(ctx, app.SourceLifecycleReconcileRequest{Scope: testScope, Desired: desired, AuthorityGeneration: app.SourceLifecycleGeneration(desired), UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	authority, err := store.SourceLifecycleAuthority(ctx, testScope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.MarkSourceLifecycleReady(ctx, app.SourceLifecycleReadyUpdate{Scope: testScope, Owner: owner, AuthorityGeneration: authority.Generation, AuthorityEpoch: authority.Epoch, ActivatedEpoch: authority.Epoch, ConfigDigest: desired[0].ConfigDigest, ReadyAt: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	fence := newHostApplyCoordinator()
	if err = bindMaintenanceTest(content, store, fence); err != nil {
		t.Fatal(err)
	}
	maintainer := &incidentMaintainer{content: content}
	ingest, err := app.NewIngestService(content, store, store, maintainer, app.IngestOptions{AutoApply: true, ApplyCoordinator: fence})
	if err != nil {
		t.Fatal(err)
	}
	var reservations []app.MaintenanceReservation
	var failureClasses []string
	for _, doc := range docs {
		reserved, reserveErr := ingest.ReserveAccepted(ctx, app.AcceptedMaintenanceRequest{Source: doc, SourceDocument: doc.SourceDocument, ContentType: doc.MediaType})
		if reserveErr != nil {
			t.Fatal(reserveErr)
		}
		reservations = append(reservations, reserved)
		claim := incidentClaim(t, store, reserved.OperationID, "before")
		_, runErr := ingest.RunToTerminal(ctx, claim)
		operation, readErr := store.Operation(ctx, testScope, reserved.OperationID)
		if runErr == nil || readErr != nil || operation.Status != domain.StatusFailed || !slices.ContainsFunc(operation.Diagnostics, func(d domain.MaintenanceDiagnostic) bool {
			return d.Code == app.SourceLifecycleUnownedCode && d.Target == "wiki-filesystem:balda-docs"
		}) {
			t.Fatalf("original blocked operation = %#v, run=%v read=%v", operation, runErr, readErr)
		}
		if operation.Failure == nil {
			t.Fatal("missing typed failure")
		}
		failureClasses = append(failureClasses, operation.Failure.Class)
	}
	incidentFinalizeDocuments(t, store, docs, reservations, at.Add(2*time.Second))
	legacyOwner := domain.SourceOwner{Adapter: domain.SourceAdapterFilesystem, SourceID: incidentSourceID}
	if _, err = store.SourceLifecycle(ctx, testScope, legacyOwner); !errors.Is(err, app.ErrSourceLifecycleUnowned) {
		t.Fatalf("missing legacy owner = %v", err)
	}
	committer, err := newHostRetirementCommitter(fence, store, content.Workspace, content)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := app.NewRetirementPageService(content, store, store, store, committer, maintainer, app.RetirementPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := app.NewSourceRetirementCoordinator(app.SourceRetirementCoordinatorDependencies{Scope: testScope, Lifecycles: store, Provenance: content.Workspace, Pages: pages, Operations: store, Projection: content.Workspace, ApplyCoordinator: fence, Now: func() time.Time { return at.Add(time.Minute) }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.SourceLifecycle(ctx, testScope, legacyOwner)
	if err != nil || recovered.State != domain.SourceLifecycleRetiring || recovered.ConfigDigest != "" || recovered.RecoveryProofDigest == "" {
		t.Fatalf("recovered owner = %#v, %v", recovered, err)
	}
	work, err := store.RetirementOperations(ctx, app.RetirementOperationReadRequest{Scope: testScope, Owner: legacyOwner, RetirementGeneration: recovered.RetirementGeneration, Limit: 10})
	if err != nil || len(work.Items) != 2 {
		t.Fatalf("retirement units = %#v, %v", work, err)
	}
	for _, item := range work.Items {
		result, runErr := pages.RunToTerminal(ctx, incidentClaim(t, store, item.Operation.ID, "retire"))
		if runErr != nil || result.Operation.Status != domain.StatusCommitted {
			t.Fatalf("retirement publication = %#v, %v", result, runErr)
		}
	}
	current, err := content.ReadPages(ctx, testScope, []domain.PageID{incidentPageID}, app.DefaultReadLimits())
	retained := []string{app.SourceRefKey(docs[0]), app.SourceRefKey(inline)}
	slices.Sort(retained)
	if err != nil || len(current) != 1 || !slices.Equal(current[0].SourceRefs, retained) || strings.TrimSpace(current[0].Body) != "# Runtime contribution catalog\n\n"+incidentRetainedFacts {
		t.Fatalf("retired canonical = %#v, %v", current, err)
	}
	if _, err = os.Stat(filepath.Join(content.Root(), "wiki/concepts/obsolete-filesystem.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("obsolete page still exists: %v", err)
	}
	retry, err := store.RetrySourceMaintenance(ctx, app.SourceMaintenanceRetryRequest{Scope: testScope, SourceID: incidentSourceID, FailureClasses: failureClasses})
	if err != nil || retry.Requeued != 3 {
		t.Fatalf("retry = %#v, %v", retry, err)
	}
	for _, id := range retry.OperationIDs {
		result, runErr := ingest.RunToTerminal(ctx, incidentClaim(t, store, id, "retry"))
		if runErr != nil || result.Operation.Status != domain.StatusCommitted {
			t.Fatalf("maintenance retry = %#v, %v", result, runErr)
		}
	}
	var retired domain.SourceLifecycle
	for range 4 {
		if _, err = coordinator.Advance(ctx); err != nil {
			t.Fatal(err)
		}
		retired, err = store.SourceLifecycle(ctx, testScope, legacyOwner)
		if err != nil {
			t.Fatal(err)
		}
		if retired.State == domain.SourceLifecycleDeleted {
			break
		}
	}
	if retired.State != domain.SourceLifecycleDeleted {
		t.Fatalf("final tombstone = %#v", retired)
	}
	operationAfter, err := store.Operation(ctx, testScope, history.ID)
	if err != nil || !reflect.DeepEqual(originalOperation, operationAfter) {
		t.Fatalf("historical operation changed: %v", err)
	}
	descriptorAfter, err := store.Execution(ctx, testScope, history.ID)
	if err != nil || !reflect.DeepEqual(originalDescriptor, descriptorAfter) {
		t.Fatalf("historical descriptor changed: %v", err)
	}
	rawAfter, err := content.ReadSource(ctx, old, app.DefaultReadLimits())
	if err != nil || !bytes.Equal(rawAfter, oldBytes) || !bytes.Equal(rawBefore, mustReadHierarchyFile(t, content, old.ManifestRef)) {
		t.Fatalf("immutable history changed: %v", err)
	}
}

func incidentAccept(t *testing.T, content *hostMaintenanceContent, adapter string, document domain.DocumentID, revision, text string) domain.AcceptedSource {
	t.Helper()
	source := domain.SourceEnvelope{Scope: testScope, Source: domain.SourceRef{Adapter: adapter, ID: string(incidentSourceID) + "/" + string(document)}, Version: domain.SourceVersion{Version: revision, Digest: digestBytes([]byte(text))}, MediaType: lifecycleMarkdownMediaType, Content: []byte(text)}
	if adapter != "inline" {
		source.SourceDocument = domain.SourceDocument{SourceID: incidentSourceID, DocumentID: document, Revision: revision, URI: "https://example.test/" + string(document)}
	}
	accepted, err := content.AcceptSource(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}
func incidentMarkdown(id domain.PageID, refs []string, body string) []byte {
	return []byte("---\nid: " + string(id) + "\ntitle: Runtime contribution catalog\ntype: concept\nsource_refs:\n  - " + strings.Join(refs, "\n  - ") + "\n---\n# Runtime contribution catalog\n\n" + body + "\n")
}
func incidentClaim(t *testing.T, store *sqlite.Store, id domain.OperationID, token string) domain.WorkClaim {
	t.Helper()
	claim, err := store.ClaimOperation(t.Context(), testScope, id, domain.WorkLease{Token: token + string(id), ExpiresAt: time.Now().UTC().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

type incidentMaintainer struct{ content *hostMaintenanceContent }

func (m *incidentMaintainer) Plan(ctx context.Context, input domain.MaintenanceInput) (domain.ModelEditPlan, error) {
	pages, err := m.content.ReadPages(ctx, testScope, []domain.PageID{incidentPageID}, app.DefaultReadLimits())
	if err != nil || len(pages) != 1 {
		return domain.ModelEditPlan{}, fmt.Errorf("incident page unavailable: %w", err)
	}
	refs := append(slices.Clone(pages[0].SourceRefs), app.SourceRefKey(input.Source))
	slices.Sort(refs)
	refs = slices.Compact(refs)
	return domain.ModelEditPlan{SchemaDigest: input.Schema.Digest, SourceRefs: refs, Edits: []domain.FileEdit{{Path: incidentPagePath, ExpectedDigest: pages[0].Digest, Content: incidentMarkdown(incidentPageID, refs, incidentRetainedFacts)}}}, nil
}
func (m *incidentMaintainer) PlanRetirement(_ context.Context, input domain.RetirementInput) (domain.RetirementModelPlan, error) {
	refs := make([]string, 0, len(input.Evidence))
	facts := make([]string, 0, len(input.Evidence))
	for _, e := range input.Evidence {
		refs = append(refs, e.SourceRef)
		facts = append(facts, e.Text)
	}
	return domain.RetirementModelPlan{SchemaDigest: input.Schema.Digest, SourceRefs: refs, Pages: []domain.FileEdit{{Path: input.Page.Path, ExpectedDigest: input.Page.Digest, Content: incidentMarkdown(input.Page.ID, refs, strings.Join(facts, " "))}}}, nil
}
func incidentFinalizeDocuments(t *testing.T, store *sqlite.Store, docs []domain.AcceptedSource, reservations []app.MaintenanceReservation, at time.Time) {
	t.Helper()
	ctx := t.Context()
	run := domain.SyncRun{ID: "incident-current-git-sync", Scope: testScope, SourceID: incidentSourceID, ConfigDigest: strings.Repeat("a", 64), Status: domain.SyncStatusScanning, StartedAt: at, UpdatedAt: at}
	if _, _, err := store.BeginSync(ctx, app.BeginSyncRequest{Run: run, Type: domain.SourceTypeGit, RepositoryIdentity: strings.Repeat("b", 64)}); err != nil {
		t.Fatal(err)
	}
	states := make([]app.PreparedDocumentState, 0, len(docs))
	for i, doc := range docs {
		states = append(states, app.PreparedDocumentState{Action: app.SyncDocumentActive, State: domain.DocumentState{Scope: testScope, SourceID: incidentSourceID, DocumentID: doc.SourceDocument.DocumentID, Revision: doc.Version.Version, AcceptedSource: doc, MaintenanceRevision: doc.Version.Version, MaintenanceOperationID: reservations[i].OperationID, MaintenanceGeneration: reservations[i].Generation, MirrorPath: "wiki/sources/balda-docs/" + string(doc.SourceDocument.DocumentID), MirrorDigest: doc.Version.Digest, LastSeenRunID: run.ID, CreatedAt: at, UpdatedAt: at}})
	}
	prepared := app.PreparedSyncState{RunID: run.ID, Scope: testScope, SourceID: incidentSourceID, CompleteScan: true, MaintenanceReady: false, Checkpoint: "incident-complete", Counts: domain.SyncCounts{Added: 3}, Documents: states, PreparedAt: at.Add(time.Second)}
	var err error
	prepared.CandidateDigest, err = app.PreparedSyncDigest(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.PrepareSync(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	transition := app.SyncGeneration{RunID: run.ID, Scope: testScope, SourceID: incidentSourceID, Generation: "incident-source-sync", UpdatedAt: at.Add(2 * time.Second)}
	if _, err = store.MarkContentCommitted(ctx, transition); err != nil {
		t.Fatal(err)
	}
	if _, err = store.MarkProjected(ctx, transition); err != nil {
		t.Fatal(err)
	}
	if _, err = store.FinalizeSync(ctx, app.SyncFinalization{RunID: run.ID, Scope: testScope, SourceID: incidentSourceID, CandidateDigest: prepared.CandidateDigest, Generation: transition.Generation, Checkpoint: prepared.Checkpoint, Counts: prepared.Counts, FinalizedAt: at.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}
}
