package fs

import (
	"container/heap"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
)

const (
	canonicalProvenanceCursorVersion  = 1
	maxCanonicalProvenanceCursorBytes = 4_096
)

var (
	_ app.CanonicalProvenanceReader = (*Workspace)(nil)
)

type canonicalProvenanceCursorState struct {
	Version             int               `json:"version"`
	Scope               string            `json:"scope"`
	Owner               knowl.SourceOwner `json:"owner"`
	PublisherEpoch      string            `json:"publisher_epoch"`
	PublisherGeneration uint64            `json:"publisher_generation"`
	InventoryVersion    string            `json:"inventory_version"`
	After               string            `json:"after"`
	RoundPages          int               `json:"round_pages"`
}

type provenanceCandidateHeap []operatorInventoryEntry

type canonicalProvenanceInventoryResult struct {
	candidates   []operatorInventoryEntry
	version      string
	scanned      int
	peak         int
	afterFound   bool
	throughAfter int
}

func (items provenanceCandidateHeap) Len() int           { return len(items) }
func (items provenanceCandidateHeap) Less(i, j int) bool { return items[i].path > items[j].path }
func (items provenanceCandidateHeap) Swap(i, j int)      { items[i], items[j] = items[j], items[i] }
func (items *provenanceCandidateHeap) Push(value any) {
	*items = append(*items, value.(operatorInventoryEntry))
}
func (items *provenanceCandidateHeap) Pop() any {
	old := *items
	last := old[len(old)-1]
	*items = old[:len(old)-1]
	return last
}

// PageProvenance returns one bounded keyset window of canonical pages citing
// one exact configured-source owner. Canonical files and accepted manifests
// are observed under the same publication lock. It acquires only the workspace
// lock, after any caller-held ApplyCoordinator, matching supported writer order.
func (workspace *Workspace) PageProvenance(
	ctx context.Context,
	scope knowl.ScopeRef,
	owner knowl.SourceOwner,
	cursor app.CanonicalProvenanceCursor,
	limits app.CanonicalProvenanceLimits,
) (app.CanonicalProvenancePage, error) {
	result := app.CanonicalProvenancePage{Items: []app.CanonicalPageProvenance{}}
	limits, err := app.NormalizeCanonicalProvenanceLimits(limits)
	if err != nil || app.ValidateSourceOwner(owner) != nil || strings.TrimSpace(string(scope)) == "" {
		return result, app.ErrCanonicalProvenanceInvalid
	}
	state, err := decodeCanonicalProvenanceCursor(cursor, scope, owner, limits)
	if err != nil {
		return result, err
	}
	result.RoundPages = state.RoundPages
	unlock, err := workspace.operatorLock(ctx, scope)
	if err != nil {
		return result, fmt.Errorf("lock canonical provenance: %w", err)
	}
	defer unlock()
	publication := workspace.publicationFenceLocked()
	if state.PublisherEpoch != "" &&
		(state.PublisherEpoch != publication.epoch || state.PublisherGeneration != publication.generation) {
		return app.CanonicalProvenancePage{Items: []app.CanonicalPageProvenance{}}, provenanceFailure(app.CanonicalProvenanceCodeRestart, "", "", app.ErrCanonicalProvenanceRestart)
	}
	state.PublisherEpoch = publication.epoch
	state.PublisherGeneration = publication.generation

	sources, err := workspace.operatorSources(ctx, scope)
	if err != nil {
		return result, fmt.Errorf("read accepted provenance: %w", err)
	}
	wiki, err := openReadRoot(workspace.root, workspaceWikiDir)
	if err != nil {
		return result, fmt.Errorf("open canonical provenance: %w", operatorReadError(err, app.ErrOperatorWorkspaceUnavailable))
	}
	defer wiki.close()

	inventory, err := canonicalProvenanceInventory(ctx, wiki, sources, state.After, limits)
	if err != nil {
		if errors.Is(err, ErrPathRejected) {
			err = provenanceFailure(app.CanonicalProvenanceCodePageMalformed, "", "", app.ErrCanonicalProvenanceMalformed)
		} else if errors.Is(err, app.ErrOperatorReadLimitExceeded) {
			err = provenanceFailure(app.CanonicalProvenanceCodeLimit, "", "", app.ErrCanonicalProvenanceLimit)
		}
		return app.CanonicalProvenancePage{Items: []app.CanonicalPageProvenance{}}, fmt.Errorf("inventory canonical provenance: %w", err)
	}
	if state.InventoryVersion != "" && state.InventoryVersion != inventory.version {
		// The trusted restart fence is the cooperating publisher generation.
		// Inventory metadata is retained only to catch common unsupported direct
		// filesystem edits; it is not an authority guarantee for such edits.
		return app.CanonicalProvenancePage{Items: []app.CanonicalPageProvenance{}}, provenanceFailure(app.CanonicalProvenanceCodeRestart, "", "", app.ErrCanonicalProvenanceRestart)
	}
	if state.After != "" && (!inventory.afterFound || state.RoundPages != inventory.throughAfter) {
		return app.CanonicalProvenancePage{Items: []app.CanonicalPageProvenance{}}, provenanceFailure(app.CanonicalProvenanceCodeCursor, state.After, "", app.ErrCanonicalProvenanceCursor)
	}
	state.InventoryVersion = inventory.version

	remainingBytes := limits.TotalBytes
	lastExamined := state.After
	candidates := inventory.candidates
	available := len(candidates)
	if available > limits.Examined {
		candidates = candidates[:limits.Examined]
	}
	for _, candidate := range candidates {
		if result.Returned >= limits.Returned {
			break
		}
		if err := contextErr(ctx); err != nil {
			return app.CanonicalProvenancePage{Items: []app.CanonicalPageProvenance{}}, err
		}
		canonicalPath := workspaceWikiDir + "/" + candidate.path
		readBytes := min(limits.PageBytes, remainingBytes)
		if readBytes <= 0 {
			return app.CanonicalProvenancePage{Items: []app.CanonicalPageProvenance{}}, provenanceFailure(app.CanonicalProvenanceCodeLimit, canonicalPath, "", app.ErrCanonicalProvenanceLimit)
		}
		page, info, err := workspace.operatorPage(wiki, candidate.path, knowl.ReadLimits{Bytes: readBytes})
		if err != nil {
			cause := app.ErrCanonicalProvenanceMalformed
			code := app.CanonicalProvenanceCodePageMalformed
			if errors.Is(err, app.ErrOperatorReadLimitExceeded) {
				cause, code = app.ErrCanonicalProvenanceLimit, app.CanonicalProvenanceCodeLimit
			}
			return app.CanonicalProvenancePage{Items: []app.CanonicalPageProvenance{}}, provenanceFailure(code, canonicalPath, "", cause)
		}
		if !operatorFileUnchanged(candidate.info, info) {
			return app.CanonicalProvenancePage{Items: []app.CanonicalPageProvenance{}}, provenanceFailure(app.CanonicalProvenanceCodeRestart, canonicalPath, "", app.ErrCanonicalProvenanceRestart)
		}
		item, err := exactCanonicalProvenance(page, sources, owner, limits.SourceRefs)
		if err != nil {
			return app.CanonicalProvenancePage{Items: []app.CanonicalPageProvenance{}}, err
		}
		result.Examined++
		result.RoundPages = state.RoundPages + result.Examined
		remainingBytes -= len(page.Content)
		lastExamined = canonicalPath
		if item != nil {
			result.Items = append(result.Items, *item)
			result.Returned++
		}
	}
	result.EOF = result.Examined == available
	result.Authoritative = result.EOF
	if !result.EOF {
		if lastExamined == "" || lastExamined <= state.After {
			return app.CanonicalProvenancePage{Items: []app.CanonicalPageProvenance{}}, provenanceFailure(app.CanonicalProvenanceCodeCursor, lastExamined, "", app.ErrCanonicalProvenanceCursor)
		}
		result.NextCursor, err = encodeCanonicalProvenanceCursor(canonicalProvenanceCursorState{
			Version: canonicalProvenanceCursorVersion, Scope: string(scope), Owner: owner,
			PublisherEpoch: publication.epoch, PublisherGeneration: publication.generation,
			InventoryVersion: inventory.version, After: lastExamined, RoundPages: result.RoundPages,
		})
		if err != nil {
			return app.CanonicalProvenancePage{Items: []app.CanonicalPageProvenance{}}, err
		}
	}
	return result, nil
}

func exactCanonicalProvenance(page knowl.PageSnapshot, sources map[string]knowl.AcceptedSource, target knowl.SourceOwner, maxRefs int) (*app.CanonicalPageProvenance, error) {
	metadata, err := knowlwiki.ParseFrontmatter(page.Content)
	if err != nil || metadata.ID != string(page.ID) || metadata.Type == "" || len(page.SourceRefs) == 0 {
		return nil, provenanceFailure(app.CanonicalProvenanceCodePageMalformed, page.Path, "", app.ErrCanonicalProvenanceMalformed)
	}
	if len(page.SourceRefs) > maxRefs {
		return nil, provenanceFailure(app.CanonicalProvenanceCodeLimit, page.Path, "", app.ErrCanonicalProvenanceLimit)
	}
	matched := false
	for _, ref := range page.SourceRefs {
		if !operatorSourceRefValid(ref) {
			return nil, provenanceFailure(app.CanonicalProvenanceCodeRefMalformed, page.Path, ref, app.ErrCanonicalProvenanceMalformed)
		}
		source, exists := sources[ref]
		if !exists || source.SourceDocument == (knowl.SourceDocument{}) {
			return nil, provenanceFailure(app.CanonicalProvenanceCodeRefUnowned, page.Path, ref, app.ErrSourceLifecycleUnowned)
		}
		owner := knowl.SourceOwner{Adapter: knowl.SourceAdapterID(source.Source.Adapter), SourceID: source.SourceDocument.SourceID}
		if app.ValidateSourceOwner(owner) != nil {
			return nil, provenanceFailure(app.CanonicalProvenanceCodeRefUnowned, page.Path, ref, app.ErrSourceLifecycleUnowned)
		}
		matched = matched || owner == target
	}
	if !matched {
		return nil, nil
	}
	return &app.CanonicalPageProvenance{
		ID: page.ID, Path: page.Path, Digest: page.Digest, SourceRefs: slices.Clone(page.SourceRefs),
	}, nil
}

func canonicalProvenanceInventory(
	ctx context.Context,
	root *readRoot,
	sources map[string]knowl.AcceptedSource,
	after string,
	limits app.CanonicalProvenanceLimits,
) (canonicalProvenanceInventoryResult, error) {
	capacity := limits.Examined + 1
	candidates := make(provenanceCandidateHeap, 0, capacity)
	pageCount, scanned, peak, throughAfter := 0, 0, 0, 0
	var pageFacts [sha256.Size]byte
	after = strings.TrimPrefix(after, workspaceWikiDir+"/")
	afterFound := after == ""
	err := root.walk(func(relative string, entry os.DirEntry) error {
		if err := contextErr(ctx); err != nil {
			return err
		}
		scanned++
		if entry.IsDir() {
			return nil
		}
		kind, err := okf.ClassifyPath(relative)
		if err != nil {
			return err
		}
		if kind != okf.DocumentConcept {
			return nil
		}
		canonicalPath := workspaceWikiDir + "/" + relative
		if len(canonicalPath) > limits.PathBytes {
			return provenanceFailure(app.CanonicalProvenanceCodeLimit, canonicalPath, "", app.ErrCanonicalProvenanceLimit)
		}
		pageCount++
		if pageCount > limits.RoundPages {
			return provenanceFailure(app.CanonicalProvenanceCodeLimit, canonicalPath, "", app.ErrCanonicalProvenanceLimit)
		}
		item, err := operatorStat(root, relative)
		if err != nil {
			return err
		}
		addCanonicalProvenanceFact(&pageFacts, []any{item.path, item.identity, item.info.Size(), item.info.ModTime().UTC().UnixNano()})
		if relative <= after {
			throughAfter++
			afterFound = afterFound || relative == after
			return nil
		}
		if len(candidates) < capacity {
			heap.Push(&candidates, item)
		} else if relative < candidates[0].path {
			heap.Pop(&candidates)
			heap.Push(&candidates, item)
		}
		peak = max(peak, len(candidates))
		return nil
	})
	if err != nil {
		return canonicalProvenanceInventoryResult{}, err
	}
	slices.SortFunc(candidates, func(left, right operatorInventoryEntry) int {
		return strings.Compare(left.path, right.path)
	})
	return canonicalProvenanceInventoryResult{
		candidates: candidates, version: canonicalProvenanceInventoryVersion(pageFacts, pageCount, sources),
		scanned: scanned, peak: peak, afterFound: afterFound, throughAfter: throughAfter,
	}, nil
}

func addCanonicalProvenanceFact(accumulator *[sha256.Size]byte, fact []any) {
	payload, _ := json.Marshal(fact)
	digest := sha256.Sum256(payload)
	carry := uint16(0)
	for index := len(accumulator) - 1; index >= 0; index-- {
		total := uint16(accumulator[index]) + uint16(digest[index]) + carry
		accumulator[index] = byte(total)
		carry = total >> 8
	}
}

func canonicalProvenanceInventoryVersion(pageFacts [sha256.Size]byte, pageCount int, sources map[string]knowl.AcceptedSource) string {
	var sourceFacts [sha256.Size]byte
	for ref, source := range sources {
		addCanonicalProvenanceFact(&sourceFacts, []any{ref, source})
	}
	payload, _ := json.Marshal([]any{
		"canonical-provenance-inventory-v1",
		pageCount,
		hex.EncodeToString(pageFacts[:]),
		len(sources),
		hex.EncodeToString(sourceFacts[:]),
	})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func encodeCanonicalProvenanceCursor(state canonicalProvenanceCursorState) (app.CanonicalProvenanceCursor, error) {
	payload, err := json.Marshal(state)
	if err != nil {
		return "", provenanceFailure(app.CanonicalProvenanceCodeCursor, "", "", app.ErrCanonicalProvenanceCursor)
	}
	// This checksum enforces byte canonicality and accidental-corruption
	// detection. The internal cursor is not an authentication boundary.
	digest := sha256.Sum256(payload)
	return app.CanonicalProvenanceCursor(base64.RawURLEncoding.EncodeToString(payload) + "." + hex.EncodeToString(digest[:16])), nil
}

func decodeCanonicalProvenanceCursor(cursor app.CanonicalProvenanceCursor, scope knowl.ScopeRef, owner knowl.SourceOwner, limits app.CanonicalProvenanceLimits) (canonicalProvenanceCursorState, error) {
	if cursor == "" {
		return canonicalProvenanceCursorState{Version: canonicalProvenanceCursorVersion, Scope: string(scope), Owner: owner}, nil
	}
	if len(cursor) > maxCanonicalProvenanceCursorBytes {
		return canonicalProvenanceCursorState{}, provenanceFailure(app.CanonicalProvenanceCodeCursor, "", "", app.ErrCanonicalProvenanceCursor)
	}
	encoded, checksum, found := strings.Cut(string(cursor), ".")
	if !found || strings.Contains(checksum, ".") {
		return canonicalProvenanceCursorState{}, provenanceFailure(app.CanonicalProvenanceCodeCursor, "", "", app.ErrCanonicalProvenanceCursor)
	}
	payload, decodeErr := base64.RawURLEncoding.DecodeString(encoded)
	want, checksumErr := hex.DecodeString(checksum)
	digest := sha256.Sum256(payload)
	if decodeErr != nil || checksumErr != nil || len(want) != 16 || subtle.ConstantTimeCompare(want, digest[:16]) != 1 {
		return canonicalProvenanceCursorState{}, provenanceFailure(app.CanonicalProvenanceCodeCursor, "", "", app.ErrCanonicalProvenanceCursor)
	}
	var state canonicalProvenanceCursorState
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	decodeErr = decoder.Decode(&state)
	var trailing any
	trailingErr := decoder.Decode(&trailing)
	publisherEpoch, publisherEpochErr := hex.DecodeString(state.PublisherEpoch)
	inventoryDigest, inventoryErr := hex.DecodeString(state.InventoryVersion)
	_, page := knowlwiki.PageIDFromPath(state.After)
	if decodeErr != nil || !errors.Is(trailingErr, io.EOF) || state.Version != canonicalProvenanceCursorVersion || state.Scope != string(scope) || state.Owner != owner ||
		state.RoundPages <= 0 || state.RoundPages > limits.RoundPages || len(state.After) > limits.PathBytes ||
		publisherEpochErr != nil || len(publisherEpoch) != workspacePublicationEpochBytes ||
		inventoryErr != nil || len(inventoryDigest) != sha256.Size ||
		validateCanonicalWikiTarget(state.After) != nil || !page {
		return canonicalProvenanceCursorState{}, provenanceFailure(app.CanonicalProvenanceCodeCursor, "", "", app.ErrCanonicalProvenanceCursor)
	}
	canonical, err := encodeCanonicalProvenanceCursor(state)
	if err != nil || canonical != cursor {
		return canonicalProvenanceCursorState{}, provenanceFailure(app.CanonicalProvenanceCodeCursor, "", "", app.ErrCanonicalProvenanceCursor)
	}
	return state, nil
}

func provenanceFailure(code, path, ref string, cause error) error {
	return &app.CanonicalProvenanceFailure{Code: code, Path: path, SourceRef: ref, Cause: cause}
}
