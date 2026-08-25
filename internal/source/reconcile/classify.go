package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"

	filesystem "github.com/baldaworks/knowl/internal/source/filesystem"
)

// sagaInput is the prepared hand-off from the scan stage to the saga tail.
type sagaInput struct {
	run         knowl.SyncRun
	prepared    app.PreparedSyncRead
	changed     bool
	mutations   []knowl.SourceMutation
	diagnostics []knowl.SourceDiagnostic
}

// reconstructPrepared rebuilds the exact prepared mutations of one
// prepared-or-later run from persisted candidates, immutable raw content, and
// the current canonical inventory, verifying every mirror identity on the way.
func (service *Service) reconstructPrepared(ctx context.Context, scope knowl.ScopeRef, source knowl.Source, run knowl.SyncRun) (sagaInput, error) {
	read, err := service.state.PreparedSync(ctx, scope, run.ID)
	if err != nil {
		return sagaInput{}, failStage(classState, err)
	}
	refs, err := service.state.ScanDocuments(ctx, scope, run.ID, service.options.MaxScanDocuments)
	if err != nil {
		return sagaInput{}, failStage(classState, err)
	}
	inventory, err := service.canonicalInventory(ctx, scope, source.ID)
	if err != nil {
		return sagaInput{}, err
	}
	var mutations []knowl.SourceMutation
	var inputDiagnostics []knowl.SourceDiagnostic
	for _, candidate := range read.Documents {
		switch candidate.Action {
		case app.SyncDocumentTombstone:
			digest, present := inventory[candidate.State.MirrorPath]
			if candidate.State.MirrorPath == "" || !present {
				return sagaInput{}, failStage(classState, errors.New("prepared tombstone lost its canonical mirror"))
			}
			mutations = append(mutations, knowl.SourceMutation{
				Action: knowl.SourceMutationDelete, Path: candidate.State.MirrorPath, ExpectedDigest: digest,
			})
		case app.SyncDocumentActive:
			ref := knowl.DocumentRef{
				ExternalID: candidate.State.DocumentID, Revision: candidate.State.Revision, Path: string(candidate.State.DocumentID),
			}
			rendered, content, diagnostics, renderErr := service.storedCandidate(ctx, source, run, ref, candidate.State.AcceptedSource, refs)
			if renderErr != nil {
				return sagaInput{}, renderErr
			}
			if rendered.State.MirrorPath != candidate.State.MirrorPath || rendered.State.MirrorDigest != candidate.State.MirrorDigest {
				return sagaInput{}, failStage(classState, errors.New("prepared mirror identity drifted"))
			}
			mutations = append(mutations, knowl.SourceMutation{
				Action: knowl.SourceMutationWrite, Path: rendered.State.MirrorPath,
				ExpectedDigest: inventory[rendered.State.MirrorPath], Content: content,
			})
			inputDiagnostics = append(inputDiagnostics, diagnostics...)
		default:
			return sagaInput{}, failStage(classState, app.ErrSourceInvalid)
		}
	}
	sort.Slice(mutations, func(left, right int) bool { return mutations[left].Path < mutations[right].Path })
	return sagaInput{run: run, prepared: read, changed: len(mutations) > 0, mutations: mutations, diagnostics: inputDiagnostics}, nil
}

// classifyAndPrepare compares the complete catalog against durable heads and
// the exact canonical inventory, derives deterministic mutations, and durably
// prepares the bounded candidate set before any canonical mutation executes.
func (service *Service) classifyAndPrepare(ctx context.Context, scope knowl.ScopeRef, adapter app.SourceAdapter, source knowl.Source, run knowl.SyncRun, refs []knowl.DocumentRef) (sagaInput, error) {
	heads, err := service.state.DocumentStates(ctx, scope, source.ID, app.DocumentListOptions{
		IncludeDeleted: true, Limit: service.options.MaxScanDocuments,
	})
	if err != nil {
		return sagaInput{}, failStage(classState, err)
	}
	activeHeads := make(map[knowl.DocumentID]knowl.DocumentState)
	tombstoned := make(map[knowl.DocumentID]knowl.DocumentState)
	for _, head := range heads {
		if head.Deleted {
			tombstoned[head.DocumentID] = head
		} else {
			activeHeads[head.DocumentID] = head
		}
	}
	inventory, err := service.canonicalInventory(ctx, scope, source.ID)
	if err != nil {
		return sagaInput{}, err
	}

	sorted := sortedRefs(refs)
	catalog := make(map[knowl.DocumentID]struct{}, len(sorted))
	candidates := make([]app.PreparedDocumentState, 0, len(sorted))
	var mutations []knowl.SourceMutation
	var diagnostics []knowl.SourceDiagnostic
	counts := knowl.SyncCounts{}
	changed := false

	for _, ref := range sorted {
		catalog[ref.ExternalID] = struct{}{}
		head, isActive := activeHeads[ref.ExternalID]
		switch {
		case !isActive:
			previous, reappeared := tombstoned[ref.ExternalID]
			if reappeared && previous.Revision == ref.Revision {
				// The earlier deletion removed the canonical mirror, so the
				// stored raw revision renders a fresh create precondition.
				candidate, content, observed, err := service.storedCandidate(ctx, source, run, ref, previous.AcceptedSource, sorted)
				if err != nil {
					return sagaInput{}, err
				}
				mutations = appendWriteMutation(mutations, candidate, inventory, content)
				diagnostics = append(diagnostics, observed...)
				candidates = append(candidates, candidate)
				counts.Updated++
				changed = true
				continue
			}
			candidate, content, observed, err := service.fetchAcceptNormalize(ctx, adapter, source, run, ref, sorted)
			if err != nil {
				return sagaInput{}, err
			}
			mutations = appendWriteMutation(mutations, candidate, inventory, content)
			diagnostics = append(diagnostics, observed...)
			candidates = append(candidates, candidate)
			changed = true
			if reappeared {
				counts.Updated++
			} else {
				counts.Added++
			}
		case head.Revision != ref.Revision:
			candidate, content, observed, err := service.fetchAcceptNormalize(ctx, adapter, source, run, ref, sorted)
			if err != nil {
				return sagaInput{}, err
			}
			mutations = appendWriteMutation(mutations, candidate, inventory, content)
			diagnostics = append(diagnostics, observed...)
			candidates = append(candidates, candidate)
			counts.Updated++
			if head.MirrorPath != candidate.State.MirrorPath || head.MirrorDigest != candidate.State.MirrorDigest {
				changed = true
			}
		default:
			candidate, content, observed, err := service.normalizeStored(ctx, source, run, ref, head, sorted)
			if err != nil {
				return sagaInput{}, err
			}
			if head.MirrorPath != candidate.State.MirrorPath || head.MirrorDigest != candidate.State.MirrorDigest {
				mutations = appendWriteMutation(mutations, candidate, inventory, content)
				changed = true
			}
			diagnostics = append(diagnostics, observed...)
			candidates = append(candidates, candidate)
			counts.Unchanged++
		}
	}

	for documentID, head := range activeHeads {
		if _, listed := catalog[documentID]; listed {
			continue
		}
		digest, present := inventory[head.MirrorPath]
		if head.MirrorPath == "" || !present {
			return sagaInput{}, failStage(classState, errors.New("active mirror missing from canonical inventory"))
		}
		mutations = append(mutations, knowl.SourceMutation{
			Action: knowl.SourceMutationDelete, Path: head.MirrorPath, ExpectedDigest: digest,
		})
		tombstone := head
		tombstone.LastSeenRunID = run.ID
		tombstone.Deleted = true
		tombstone.DeletedAt = service.options.Clock()
		candidates = append(candidates, app.PreparedDocumentState{Action: app.SyncDocumentTombstone, State: tombstone})
		counts.Deleted++
		changed = true
	}

	if err := assertNoForeignCanonicalPaths(inventory, mutations, activeHeads); err != nil {
		return sagaInput{}, err
	}

	sort.Slice(candidates, func(left, right int) bool {
		return candidates[left].State.DocumentID < candidates[right].State.DocumentID
	})
	sort.Slice(mutations, func(left, right int) bool { return mutations[left].Path < mutations[right].Path })
	checkpoint := scanCheckpoint(sorted)
	prepared := app.PreparedSyncState{
		RunID: run.ID, Scope: scope, SourceID: source.ID, CompleteScan: true,
		Checkpoint: checkpoint, Counts: counts, Documents: candidates, PreparedAt: service.options.Clock(),
	}
	digest, err := app.PreparedSyncDigest(prepared)
	if err != nil {
		return sagaInput{}, failStage(classScan, err)
	}
	prepared.CandidateDigest = digest
	preparedRun, err := service.state.PrepareSync(ctx, prepared)
	if err != nil {
		return sagaInput{}, failStage(classState, err)
	}
	return sagaInput{
		run:         preparedRun,
		prepared:    app.PreparedSyncRead{RunID: run.ID, Scope: scope, SourceID: source.ID, Checkpoint: checkpoint, Counts: counts, Documents: candidates, CandidateDigest: digest},
		changed:     changed,
		mutations:   mutations,
		diagnostics: diagnostics,
	}, nil
}

// appendWriteMutation derives the exact canonical write precondition for one
// freshly rendered candidate and attaches the rendered content.
func appendWriteMutation(mutations []knowl.SourceMutation, candidate app.PreparedDocumentState, inventory map[string]string, content []byte) []knowl.SourceMutation {
	path := candidate.State.MirrorPath
	if path == "" || content == nil {
		return mutations
	}
	return append(mutations, knowl.SourceMutation{
		Action: knowl.SourceMutationWrite, Path: path,
		ExpectedDigest: inventory[path], Content: content,
	})
}

// canonicalInventory reads the exact bounded canonical namespace snapshot.
func (service *Service) canonicalInventory(ctx context.Context, scope knowl.ScopeRef, sourceID knowl.SourceID) (map[string]string, error) {
	entries, err := service.sourceContent.SourceDigests(ctx, scope, sourceID, service.options.MaxMutations)
	if err != nil {
		return nil, failStage(classState, err)
	}
	inventory := make(map[string]string, len(entries))
	for _, entry := range entries {
		inventory[entry.Path] = entry.Digest
	}
	return inventory, nil
}

func sortedRefs(refs []knowl.DocumentRef) []knowl.DocumentRef {
	sorted := make([]knowl.DocumentRef, len(refs))
	copy(sorted, refs)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left].Path < sorted[right].Path })
	return sorted
}

// fetchAcceptNormalize selectively fetches one descriptor, accepts its exact
// immutable raw revision, and renders the canonical mirror candidate.
func (service *Service) fetchAcceptNormalize(ctx context.Context, adapter app.SourceAdapter, source knowl.Source, run knowl.SyncRun, ref knowl.DocumentRef, catalogRefs []knowl.DocumentRef) (app.PreparedDocumentState, []byte, []knowl.SourceDiagnostic, error) {
	document, err := adapter.Fetch(ctx, source, ref)
	if err != nil {
		return app.PreparedDocumentState{}, nil, nil, failStage(classFetch, err)
	}
	if document.ExternalID != ref.ExternalID || document.Revision != ref.Revision || document.Path != ref.Path ||
		app.ValidateDocument(document, service.options.MaxRawBytes) != nil {
		return app.PreparedDocumentState{}, nil, nil, failStage(classFetch, app.ErrSourceInvalid)
	}
	envelope := knowl.SourceEnvelope{
		Scope:      run.Scope,
		Source:     knowl.SourceRef{Adapter: filesystemAdapterName, ID: string(source.ID) + "/" + string(document.ExternalID)},
		Version:    knowl.SourceVersion{Version: document.Revision, Digest: contentDigest(document.Content)},
		MediaType:  document.MediaType,
		Content:    document.Content,
		ReceivedAt: service.options.Clock(),
	}
	accepted, err := service.content.AcceptSource(ctx, envelope)
	if err != nil {
		return app.PreparedDocumentState{}, nil, nil, failStage(classRaw, err)
	}
	config := *source.Config.Filesystem
	rendered := knowl.Document{
		DocumentRef: knowl.DocumentRef{ExternalID: ref.ExternalID, Revision: ref.Revision, Path: ref.Path},
		Title:       filesystem.DocumentTitle(ref.Path, document.Content),
		URI:         filesystem.DocumentURI(config, ref.Path),
		MediaType:   accepted.MediaType,
		Content:     document.Content,
	}
	if app.ValidateDocument(rendered, service.options.MaxRawBytes) != nil {
		return app.PreparedDocumentState{}, nil, nil, failStage(classNormalize, app.ErrSourceInvalid)
	}
	candidate, content, diagnostics, err := service.renderCandidateWith(ctx, source, run, rendered, accepted, catalogRefs)
	if err != nil {
		return app.PreparedDocumentState{}, nil, nil, err
	}
	return candidate, content, diagnostics, nil
}

// normalizeStored rerenders one stored raw revision against the fresh catalog
// without any Fetch; provenance identity stays exactly as previously accepted.
func (service *Service) normalizeStored(ctx context.Context, source knowl.Source, run knowl.SyncRun, ref knowl.DocumentRef, head knowl.DocumentState, catalogRefs []knowl.DocumentRef) (app.PreparedDocumentState, []byte, []knowl.SourceDiagnostic, error) {
	return service.storedCandidate(ctx, source, run, ref, head.AcceptedSource, catalogRefs)
}

func (service *Service) storedCandidate(ctx context.Context, source knowl.Source, run knowl.SyncRun, ref knowl.DocumentRef, accepted knowl.AcceptedSource, catalogRefs []knowl.DocumentRef) (app.PreparedDocumentState, []byte, []knowl.SourceDiagnostic, error) {
	raw, err := service.content.ReadSource(ctx, accepted, knowl.ReadLimits{Bytes: service.options.MaxRawBytes})
	if err != nil {
		return app.PreparedDocumentState{}, nil, nil, failStage(classRaw, err)
	}
	config := *source.Config.Filesystem
	document := knowl.Document{
		DocumentRef: knowl.DocumentRef{ExternalID: ref.ExternalID, Revision: ref.Revision, Path: ref.Path},
		Title:       filesystem.DocumentTitle(ref.Path, raw),
		URI:         filesystem.DocumentURI(config, ref.Path),
		MediaType:   filesystem.DocumentMediaType(ref.Path),
		Content:     raw,
	}
	if app.ValidateDocument(document, service.options.MaxRawBytes) != nil {
		return app.PreparedDocumentState{}, nil, nil, failStage(classNormalize, app.ErrSourceInvalid)
	}
	return service.renderCandidateWith(ctx, source, run, document, accepted, catalogRefs)
}

// renderCandidateWith normalizes one raw document into its detached mirror
// candidate plus the exact rendered bytes for the canonical write.
func (service *Service) renderCandidateWith(ctx context.Context, source knowl.Source, run knowl.SyncRun, document knowl.Document, accepted knowl.AcceptedSource, catalogRefs []knowl.DocumentRef) (app.PreparedDocumentState, []byte, []knowl.SourceDiagnostic, error) {
	if err := ctx.Err(); err != nil {
		return app.PreparedDocumentState{}, nil, nil, failStage(classCanceled, err)
	}
	result, err := service.normalizer.NormalizeSource(ctx, app.SourceNormalizationInput{
		Source: source, Document: document, RawSource: accepted, Catalog: catalogRefs,
	})
	if err != nil {
		return app.PreparedDocumentState{}, nil, nil, failStage(classNormalize, err)
	}
	if len(result.Mutations) != 1 || result.Mutations[0].Action != knowl.SourceMutationWrite ||
		len(result.Mutations[0].Content) == 0 {
		return app.PreparedDocumentState{}, nil, nil, failStage(classNormalize, errors.New("unexpected normalization shape"))
	}
	state := knowl.DocumentState{
		Scope: run.Scope, SourceID: source.ID, DocumentID: document.ExternalID,
		Revision: document.Revision, AcceptedSource: accepted,
		MirrorPath: result.Mutations[0].Path, MirrorDigest: result.MirrorDigest, LastSeenRunID: run.ID,
	}
	return app.PreparedDocumentState{Action: app.SyncDocumentActive, State: state}, result.Mutations[0].Content, result.Diagnostics, nil
}

func assertNoForeignCanonicalPaths(inventory map[string]string, mutations []knowl.SourceMutation, activeHeads map[knowl.DocumentID]knowl.DocumentState) error {
	referenced := make(map[string]struct{}, len(inventory)+len(activeHeads))
	for _, mutation := range mutations {
		referenced[mutation.Path] = struct{}{}
	}
	for _, head := range activeHeads {
		if head.MirrorPath != "" {
			referenced[head.MirrorPath] = struct{}{}
		}
	}
	for path := range inventory {
		if _, ok := referenced[path]; !ok {
			return failStage(classState, errors.New("canonical file outside the classified catalog"))
		}
	}
	return nil
}

// scanCheckpoint derives a deterministic bounded scan identity.
func scanCheckpoint(refs []knowl.DocumentRef) string {
	type entry struct {
		ID       string `json:"id"`
		Revision string `json:"revision"`
	}
	payload := make([]entry, 0, len(refs))
	for _, ref := range refs {
		payload = append(payload, entry{ID: string(ref.ExternalID), Revision: ref.Revision})
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func contentDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
