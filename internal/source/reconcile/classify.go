package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime"
	"sort"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/rs/zerolog/log"

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

// reconstructPrepared rebuilds legacy-mirror cleanup for one prepared run.
// Candidate source and maintenance identities are already durable; raw bytes
// never need to be fetched or normalized again.
func (service *Service) reconstructPrepared(ctx context.Context, scope knowl.ScopeRef, source knowl.Source, run knowl.SyncRun) (sagaInput, error) {
	read, err := service.state.PreparedSync(ctx, scope, run.ID)
	if err != nil {
		return sagaInput{}, failStage(classState, err)
	}
	inventory, err := service.canonicalInventory(ctx, scope, source.ID)
	if err != nil {
		return sagaInput{}, err
	}
	mutations := cleanupMutations(inventory)
	changed := len(mutations) > 0 || read.Counts.Added > 0 || read.Counts.Updated > 0 || read.Counts.Deleted > 0
	return sagaInput{run: run, prepared: read, changed: changed, mutations: mutations}, nil
}

// classifyAndPrepare accepts exact raw revisions, reserves textual maintenance,
// and prepares source state plus bounded legacy-mirror cleanup. It never renders
// configured-source bytes into the semantic wiki.
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
	counts := knowl.SyncCounts{}
	changed := false

	for _, ref := range sorted {
		catalog[ref.ExternalID] = struct{}{}
		head, isActive := activeHeads[ref.ExternalID]
		switch {
		case !isActive:
			previous, reappeared := tombstoned[ref.ExternalID]
			if reappeared && previous.Revision == ref.Revision {
				candidate, _, err := service.candidateFromAccepted(ctx, source, run, ref, previous.AcceptedSource, &previous)
				if err != nil {
					return sagaInput{}, err
				}
				candidates = append(candidates, candidate)
				counts.Updated++
				changed = true
				continue
			}
			candidate, err := service.fetchAcceptCandidate(ctx, adapter, source, run, ref)
			if err != nil {
				return sagaInput{}, err
			}
			candidates = append(candidates, candidate)
			changed = true
			if reappeared {
				counts.Updated++
			} else {
				counts.Added++
			}
		case head.Revision != ref.Revision:
			candidate, err := service.fetchAcceptCandidate(ctx, adapter, source, run, ref)
			if err != nil {
				return sagaInput{}, err
			}
			candidates = append(candidates, candidate)
			counts.Updated++
			changed = true
		default:
			candidate, reserved, err := service.candidateFromAccepted(ctx, source, run, ref, head.AcceptedSource, &head)
			if err != nil {
				return sagaInput{}, err
			}
			candidates = append(candidates, candidate)
			counts.Unchanged++
			changed = changed || reserved
		}
	}

	for documentID, head := range activeHeads {
		if _, listed := catalog[documentID]; listed {
			continue
		}
		tombstone := head
		tombstone.LastSeenRunID = run.ID
		tombstone.Deleted = true
		tombstone.DeletedAt = service.options.Clock()
		tombstone.MirrorPath = ""
		tombstone.MirrorDigest = ""
		candidates = append(candidates, app.PreparedDocumentState{Action: app.SyncDocumentTombstone, State: tombstone})
		counts.Deleted++
		changed = true
	}

	mutations := cleanupMutations(inventory)
	changed = changed || len(mutations) > 0
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
		run:       preparedRun,
		prepared:  app.PreparedSyncRead{RunID: run.ID, Scope: scope, SourceID: source.ID, Checkpoint: checkpoint, Counts: counts, Documents: candidates, CandidateDigest: digest},
		changed:   changed,
		mutations: mutations,
	}, nil
}

func cleanupMutations(inventory map[string]string) []knowl.SourceMutation {
	mutations := make([]knowl.SourceMutation, 0, len(inventory))
	for path, digest := range inventory {
		mutations = append(mutations, knowl.SourceMutation{Action: knowl.SourceMutationDelete, Path: path, ExpectedDigest: digest})
	}
	sort.Slice(mutations, func(left, right int) bool { return mutations[left].Path < mutations[right].Path })
	return mutations
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

// fetchAcceptCandidate selectively fetches and accepts one exact immutable raw
// revision before reserving textual maintenance.
func (service *Service) fetchAcceptCandidate(ctx context.Context, adapter app.SourceAdapter, source knowl.Source, run knowl.SyncRun, ref knowl.DocumentRef) (app.PreparedDocumentState, error) {
	document, err := adapter.Fetch(ctx, source, ref)
	if err != nil {
		return app.PreparedDocumentState{}, failStage(classFetch, err)
	}
	if document.ExternalID != ref.ExternalID || document.Revision != ref.Revision || document.Path != ref.Path ||
		app.ValidateDocument(document, service.options.MaxRawBytes) != nil {
		return app.PreparedDocumentState{}, failStage(classFetch, app.ErrSourceInvalid)
	}
	envelope := knowl.SourceEnvelope{
		Scope:     run.Scope,
		Source:    knowl.SourceRef{Adapter: filesystemAdapterName, ID: string(source.ID) + "/" + string(document.ExternalID)},
		Version:   knowl.SourceVersion{Version: document.Revision, Digest: contentDigest(document.Content)},
		MediaType: document.MediaType,
		SourceDocument: knowl.SourceDocument{
			SourceID: source.ID, DocumentID: document.ExternalID, Revision: document.Revision, URI: document.URI,
		},
		Content:    document.Content,
		ReceivedAt: service.options.Clock(),
	}
	accepted, err := service.content.AcceptSource(ctx, envelope)
	if err != nil {
		return app.PreparedDocumentState{}, failStage(classRaw, err)
	}
	candidate, _, err := service.candidateFromAccepted(ctx, source, run, ref, accepted, nil)
	return candidate, err
}

func (service *Service) candidateFromAccepted(ctx context.Context, source knowl.Source, run knowl.SyncRun, ref knowl.DocumentRef, accepted knowl.AcceptedSource, previous *knowl.DocumentState) (app.PreparedDocumentState, bool, error) {
	config := *source.Config.Filesystem
	document, err := app.ResolveSourceDocument(source.ID, accepted, knowl.SourceDocument{
		SourceID: source.ID, DocumentID: ref.ExternalID, Revision: ref.Revision,
		URI: filesystem.DocumentURI(config, ref.Path),
	})
	if err != nil {
		return app.PreparedDocumentState{}, false, failStage(classRaw, err)
	}
	accepted.SourceDocument = document
	state := knowl.DocumentState{
		Scope: run.Scope, SourceID: source.ID, DocumentID: ref.ExternalID, Revision: ref.Revision,
		AcceptedSource: accepted, LastSeenRunID: run.ID,
	}
	if previous != nil {
		state.MaintenanceRevision = previous.MaintenanceRevision
		state.MaintenanceOperationID = previous.MaintenanceOperationID
		state.CreatedAt = previous.CreatedAt
	}
	textual, err := textualMediaType(accepted.MediaType)
	if err != nil {
		return app.PreparedDocumentState{}, false, failStage(classRaw, err)
	}
	reserved := false
	if textual && (state.MaintenanceRevision != ref.Revision || state.MaintenanceOperationID == "") {
		reservation, reserveErr := service.maintenance.ReserveAccepted(ctx, app.AcceptedMaintenanceRequest{
			Source: accepted, SourceDocument: document, ContentType: accepted.MediaType,
		})
		if reserveErr != nil {
			log.Warn().Str("source_id", string(document.SourceID)).Str("document_id", string(document.DocumentID)).
				Str("revision", document.Revision).Str("failure_class", classMaintenance).
				Msg("knowl maintenance reservation failed")
			return app.PreparedDocumentState{}, false, failStage(classMaintenance, reserveErr)
		}
		state.MaintenanceRevision = ref.Revision
		state.MaintenanceOperationID = reservation.OperationID
		outcome := "queued"
		if reservation.Replayed {
			outcome = "replayed"
		}
		log.Info().Str("source_id", string(document.SourceID)).Str("document_id", string(document.DocumentID)).
			Str("revision", document.Revision).Str("operation_id", string(reservation.OperationID)).
			Str("maintenance_outcome", outcome).Msg("knowl maintenance reserved")
		reserved = true
	}
	return app.PreparedDocumentState{Action: app.SyncDocumentActive, State: state}, reserved, nil
}

func textualMediaType(value string) (bool, error) {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false, app.ErrSourceInvalid
	}
	return strings.HasPrefix(strings.ToLower(mediaType), "text/"), nil
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
