package app

import (
	"context"
	"crypto/rand"
	"errors"
	"path"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// A composite adapter:identity@revision key includes more than the bounded
// revision alone. Keep its separate cap within the total operator query budget.
const maxOperatorSourceRefBytes = 8 << 10
const operatorWindowsOS = "windows"

var (
	ErrOperatorInvalidRequest         = errors.New("invalid operator request")
	ErrOperatorLimitInvalid           = errors.New("invalid operator limit")
	ErrOperatorCursorInvalid          = errors.New("invalid operator cursor")
	ErrOperatorCatalogNotFound        = errors.New("operator catalog not found")
	ErrOperatorDirectoryNotFound      = errors.New("operator wiki directory not found")
	ErrOperatorSourceRevisionNotFound = errors.New("operator source revision not found")
	ErrOperatorSnapshotChanged        = errors.New("operator canonical snapshot changed")
	ErrOperatorReadLimitExceeded      = errors.New("operator read limit exceeded")
	ErrOperatorUnsupportedFormat      = errors.New("operator unsupported format")
	ErrOperatorCapabilityUnavailable  = errors.New("operator read capability unavailable")
	ErrOperatorNotReady               = errors.New("operator runtime not ready")
	ErrOperatorWorkspaceUnavailable   = errors.New("operator workspace unavailable")
)

// OperatorListOptions specifies bounded pagination without a scope selector.
// Zero Limit means omitted; HTTP adapters reject explicitly supplied zero.
type OperatorListOptions struct {
	Limit  int
	Cursor string
}

// OperatorOperationListOptions filters durable facts by status and stored source association.
type OperatorOperationListOptions struct {
	OperatorListOptions
	Status   knowl.OperationStatus
	SourceID knowl.SourceID
}

// OperatorOptions contains trusted host-owned read bounds.
type OperatorOptions struct{ ReadLimits knowl.ReadLimits }

// OperatorService dispatches narrow read capabilities under one host-bound scope.
// It has no mutation, scheduling, model-provider or maintenance dependencies.
type OperatorService struct {
	scope     knowl.ScopeRef
	readers   OperatorReaders
	limits    knowl.ReadLimits
	cursorKey [32]byte
}

// NewOperatorService binds independently optional readers to a trusted host scope.
// Its random signing key lives only for this service's lifetime.
func NewOperatorService(scope knowl.ScopeRef, readers OperatorReaders, options OperatorOptions) (*OperatorService, error) {
	if strings.TrimSpace(string(scope)) == "" || !validOpaque(string(scope), maxOperationScopeBytes, false) {
		return nil, ErrOperatorInvalidRequest
	}
	limits, err := normalizeReadLimits(options.ReadLimits)
	if err != nil {
		return nil, ErrOperatorInvalidRequest
	}
	service := &OperatorService{scope: scope, readers: readers, limits: limits}
	if _, err := rand.Read(service.cursorKey[:]); err != nil {
		return nil, ErrOperatorWorkspaceUnavailable
	}
	return service, nil
}

// CatalogChildren reads the root (empty parent) or one validated direct catalog.
func (service *OperatorService) CatalogChildren(ctx context.Context, parent knowl.PageID, options OperatorListOptions) (knowl.OperatorCatalog, error) {
	if parent != "" && !validOperatorPageID(parent) {
		return knowl.OperatorCatalog{}, ErrOperatorInvalidRequest
	}
	readOptions, err := service.listOptions(ctx, operatorCatalogEndpoint, string(parent), options)
	if err != nil {
		return knowl.OperatorCatalog{}, err
	}
	if service.readers.Catalogs == nil {
		return knowl.OperatorCatalog{}, ErrOperatorCapabilityUnavailable
	}
	readCtx, cancel := boundedReadContext(nonNilContext(ctx), service.limits)
	defer cancel()
	result, err := service.readers.Catalogs.CatalogChildren(readCtx, service.scope, parent, readOptions)
	if err != nil {
		return knowl.OperatorCatalog{}, err
	}
	if result.Parent.ID == "" {
		return knowl.OperatorCatalog{}, ErrOperatorCatalogNotFound
	}
	children, err := operatorListResult(service, operatorCatalogEndpoint, string(parent), readOptions, result.Children, true)
	if err != nil {
		return knowl.OperatorCatalog{}, err
	}
	return knowl.OperatorCatalog{Parent: result.Parent, Items: children.Items, NextCursor: children.NextCursor, SnapshotVersion: children.SnapshotVersion}, nil
}

// PageSummaries reads a consistent bounded factual-page inventory.
func (service *OperatorService) PageSummaries(ctx context.Context, options OperatorListOptions) (knowl.OperatorList[knowl.OperatorPageSummary], error) {
	return operatorListRead(service, ctx, operatorPagesEndpoint, "", options, service.readers.Pages != nil, true, func(readCtx context.Context, readOptions OperatorReadOptions) (OperatorReadPage[knowl.OperatorPageSummary], error) {
		return service.readers.Pages.PageSummaries(readCtx, service.scope, readOptions)
	})
}

// WikiDirectoryChildren reads one bounded branch of the published wiki path
// tree. The empty directory selects the root, not a page-path alias.
func (service *OperatorService) WikiDirectoryChildren(ctx context.Context, directory string, options OperatorListOptions) (knowl.OperatorList[knowl.OperatorWikiEntry], error) {
	if !validOperatorDirectory(directory) {
		return knowl.OperatorList[knowl.OperatorWikiEntry]{}, ErrOperatorInvalidRequest
	}
	if directory != "" && strings.Count(directory, "/")+1 > DefaultCatalogLimits().MaxDepth {
		return knowl.OperatorList[knowl.OperatorWikiEntry]{}, ErrOperatorReadLimitExceeded
	}
	return operatorListRead(service, ctx, operatorDirectoryEndpoint, directory, options, service.readers.Directories != nil, true, func(readCtx context.Context, readOptions OperatorReadOptions) (OperatorReadPage[knowl.OperatorWikiEntry], error) {
		return service.readers.Directories.WikiDirectoryChildren(readCtx, service.scope, directory, readOptions)
	})
}

func validOperatorDirectory(directory string) bool {
	if directory == "" {
		return true
	}
	return ValidOperatorReadPath(directory)
}

// ValidOperatorReadPath checks literal canonical relative paths before a
// descriptor-relative open. Percent is permitted in real filenames, except
// alias-shaped escapes that could become separators or dot segments after an
// extra URL decode. Windows colons remain forbidden because of ADS semantics.
func ValidOperatorReadPath(relative string) bool {
	if relative == "" || relative == "." || len(relative) > maxEditPathBytes || !utf8.ValidString(relative) || path.IsAbs(relative) || path.Clean(relative) != relative || strings.Contains(relative, "\\") || (runtime.GOOS == operatorWindowsOS && strings.Contains(relative, ":")) || encodedOperatorAlias(relative) {
		return false
	}
	for _, part := range strings.Split(relative, "/") {
		if part == "" || part == "." || part == ".." || strings.TrimSpace(part) != part || strings.HasSuffix(part, ".") {
			return false
		}
	}
	for _, character := range relative {
		if character < ' ' || character == 0x7f {
			return false
		}
	}
	return true
}

func encodedOperatorAlias(relative string) bool {
	for {
		var decoded strings.Builder
		changed := false
		for index := 0; index < len(relative); index++ {
			if relative[index] != '%' || index+2 >= len(relative) {
				decoded.WriteByte(relative[index])
				continue
			}
			value, err := strconv.ParseUint(relative[index+1:index+3], 16, 8)
			if err != nil {
				decoded.WriteByte(relative[index])
				continue
			}
			character := byte(value)
			if character == '/' || character == '\\' || character < ' ' || character == 0x7f || (runtime.GOOS == operatorWindowsOS && character == ':') {
				return true
			}
			if character == '%' || character == '.' {
				decoded.WriteByte(character)
				changed = true
			} else {
				decoded.WriteString(relative[index : index+3])
			}
			index += 2
		}
		if !changed {
			return false
		}
		relative = decoded.String()
		for _, part := range strings.Split(relative, "/") {
			if part == "." || part == ".." || (runtime.GOOS == operatorWindowsOS && strings.HasSuffix(part, ".")) {
				return true
			}
		}
	}
}

// Page returns detached current Markdown, metadata and resolved page-level provenance.
func (service *OperatorService) Page(ctx context.Context, id knowl.PageID) (knowl.OperatorPage, error) {
	if !validOperatorPageID(id) {
		return knowl.OperatorPage{}, ErrOperatorInvalidRequest
	}
	if err := contextErr(ctx); err != nil {
		return knowl.OperatorPage{}, err
	}
	if service.readers.Page == nil {
		return knowl.OperatorPage{}, ErrOperatorCapabilityUnavailable
	}
	readCtx, cancel := boundedReadContext(nonNilContext(ctx), service.limits)
	defer cancel()
	page, err := service.readers.Page.Page(readCtx, service.scope, id, service.limits)
	if err != nil {
		return knowl.OperatorPage{}, err
	}
	if page.ID == "" {
		return knowl.OperatorPage{}, ErrPageNotFound
	}
	if page.ID != id {
		return knowl.OperatorPage{}, ErrOperatorWorkspaceUnavailable
	}
	if operatorTextExceeded(page.Markdown, service.limits) {
		return knowl.OperatorPage{}, ErrOperatorReadLimitExceeded
	}
	page.RelatedPageIDs = slices.Clone(page.RelatedPageIDs)
	page.Sources = slices.Clone(page.Sources)
	if page.Metadata != nil {
		metadata := *page.Metadata
		metadata.Tags = slices.Clone(metadata.Tags)
		page.Metadata = &metadata
	}
	return page, nil
}

// SourceRevision returns an accepted immutable text revision, never adapter fetches.
func (service *OperatorService) SourceRevision(ctx context.Context, ref string) (knowl.OperatorSourceRevision, error) {
	if !validOperatorSourceRef(ref) {
		return knowl.OperatorSourceRevision{}, ErrOperatorInvalidRequest
	}
	if err := contextErr(ctx); err != nil {
		return knowl.OperatorSourceRevision{}, err
	}
	if service.readers.Revisions == nil {
		return knowl.OperatorSourceRevision{}, ErrOperatorCapabilityUnavailable
	}
	readCtx, cancel := boundedReadContext(nonNilContext(ctx), service.limits)
	defer cancel()
	revision, err := service.readers.Revisions.SourceRevision(readCtx, service.scope, ref, service.limits)
	if err != nil {
		return knowl.OperatorSourceRevision{}, err
	}
	if revision.SourceRef == "" {
		return knowl.OperatorSourceRevision{}, ErrOperatorSourceRevisionNotFound
	}
	if revision.SourceRef != ref {
		return knowl.OperatorSourceRevision{}, ErrOperatorWorkspaceUnavailable
	}
	if operatorTextExceeded(revision.Text, service.limits) {
		return knowl.OperatorSourceRevision{}, ErrOperatorReadLimitExceeded
	}
	return revision, nil
}

// Sources returns configured identities and safe durable status projections.
func (service *OperatorService) Sources(ctx context.Context, options OperatorListOptions) (knowl.OperatorList[knowl.OperatorSourceSummary], error) {
	result, err := operatorListRead(service, ctx, "sources", "", options, service.readers.Sources != nil, false, func(readCtx context.Context, readOptions OperatorReadOptions) (OperatorReadPage[knowl.OperatorSourceSummary], error) {
		return service.readers.Sources.ListSources(readCtx, service.scope, readOptions)
	})
	if err != nil {
		return knowl.OperatorList[knowl.OperatorSourceSummary]{}, err
	}
	for index := range result.Items {
		result.Items[index] = cloneOperatorSource(result.Items[index])
	}
	return result, nil
}

// Source reads safe configured status plus independently optional document summaries.
func (service *OperatorService) Source(ctx context.Context, id knowl.SourceID, options OperatorListOptions) (knowl.OperatorSourceDetail, error) {
	if ValidateSourceID(id) != nil {
		return knowl.OperatorSourceDetail{}, ErrOperatorInvalidRequest
	}
	readOptions, err := service.listOptions(ctx, "source-documents", string(id), options)
	if err != nil {
		return knowl.OperatorSourceDetail{}, err
	}
	if service.readers.Sources == nil || service.readers.Documents == nil {
		return knowl.OperatorSourceDetail{}, ErrOperatorCapabilityUnavailable
	}
	readCtx, cancel := boundedReadContext(nonNilContext(ctx), service.limits)
	defer cancel()
	source, err := service.readers.Sources.Source(readCtx, service.scope, id)
	if err != nil {
		return knowl.OperatorSourceDetail{}, err
	}
	if source.ID == "" {
		return knowl.OperatorSourceDetail{}, ErrSourceNotFound
	}
	if source.ID != id {
		return knowl.OperatorSourceDetail{}, ErrOperatorWorkspaceUnavailable
	}
	documents, err := service.readers.Documents.ListSourceDocuments(readCtx, service.scope, id, readOptions)
	if err != nil {
		return knowl.OperatorSourceDetail{}, err
	}
	result, err := operatorListResult(service, "source-documents", string(id), readOptions, documents, false)
	if err != nil {
		return knowl.OperatorSourceDetail{}, err
	}
	return knowl.OperatorSourceDetail{Source: cloneOperatorSource(source), Documents: result}, nil
}

// Operations reads durable summaries with normalized stored-association filters.
func (service *OperatorService) Operations(ctx context.Context, options OperatorOperationListOptions) (knowl.OperatorList[knowl.OperatorOperationSummary], error) {
	if !validOperatorStatus(options.Status) || (options.SourceID != "" && ValidateSourceID(options.SourceID) != nil) {
		return knowl.OperatorList[knowl.OperatorOperationSummary]{}, ErrOperatorInvalidRequest
	}
	filter := string(options.Status) + "\x00" + string(options.SourceID)
	return operatorListRead(service, ctx, operatorOperationsEndpoint, filter, options.OperatorListOptions, service.readers.Operations != nil, false, func(readCtx context.Context, readOptions OperatorReadOptions) (OperatorReadPage[knowl.OperatorOperationSummary], error) {
		return service.readers.Operations.ListOperations(readCtx, service.scope, OperatorOperationReadOptions{OperatorReadOptions: readOptions, Status: options.Status, SourceID: options.SourceID})
	})
}

func operatorListRead[T any](service *OperatorService, ctx context.Context, endpoint, filter string, options OperatorListOptions, available, canonical bool, read func(context.Context, OperatorReadOptions) (OperatorReadPage[T], error)) (knowl.OperatorList[T], error) {
	readOptions, err := service.listOptions(ctx, endpoint, filter, options)
	if err != nil {
		return knowl.OperatorList[T]{}, err
	}
	if !available {
		return knowl.OperatorList[T]{}, ErrOperatorCapabilityUnavailable
	}
	readCtx, cancel := boundedReadContext(nonNilContext(ctx), service.limits)
	defer cancel()
	page, err := read(readCtx, readOptions)
	if err != nil {
		return knowl.OperatorList[T]{}, err
	}
	return operatorListResult(service, endpoint, filter, readOptions, page, canonical)
}

func operatorListResult[T any](service *OperatorService, endpoint, filter string, options OperatorReadOptions, page OperatorReadPage[T], canonical bool) (knowl.OperatorList[T], error) {
	if len(page.Items) > options.Limit {
		return knowl.OperatorList[T]{}, ErrOperatorReadLimitExceeded
	}
	if canonical && !validExecutionDigest(page.SnapshotVersion) {
		return knowl.OperatorList[T]{}, ErrOperatorWorkspaceUnavailable
	}
	if canonical && options.Continuation.SnapshotVersion != "" && options.Continuation.SnapshotVersion != page.SnapshotVersion {
		return knowl.OperatorList[T]{}, ErrOperatorSnapshotChanged
	}
	if !validOpaque(page.NextKey, operatorContinuationKeyLimit(endpoint), true) {
		return knowl.OperatorList[T]{}, ErrOperatorReadLimitExceeded
	}
	result := knowl.OperatorList[T]{Items: append(make([]T, 0, len(page.Items)), page.Items...), SnapshotVersion: page.SnapshotVersion}
	if page.NextKey != "" {
		if endpoint == operatorOperationsEndpoint {
			if _, err := DecodeOperatorOperationPosition(page.NextKey); err != nil {
				return knowl.OperatorList[T]{}, ErrOperatorWorkspaceUnavailable
			}
		}
		cursor, err := service.encodeCursor(endpoint, filter, options.Limit, OperatorContinuation{Key: page.NextKey, SnapshotVersion: page.SnapshotVersion})
		if err != nil {
			return knowl.OperatorList[T]{}, err
		}
		result.NextCursor = cursor
	}
	return result, nil
}

func cloneOperatorSource(source knowl.OperatorSourceSummary) knowl.OperatorSourceSummary {
	if source.Status != nil {
		status := *source.Status
		source.Status = &status
	}
	return source
}
func operatorTextExceeded(text string, limits knowl.ReadLimits) bool {
	return len(text) > limits.Bytes || utf8.RuneCountInString(text) > limits.Characters
}
func validOperatorPageID(id knowl.PageID) bool {
	value := string(id)
	return validOpaque(value, maxEditPathBytes, false) && ValidOperatorReadPath(value)
}
func validOperatorSourceRef(value string) bool {
	if !validOpaque(value, maxOperatorSourceRefBytes, false) || strings.TrimSpace(value) != value {
		return false
	}
	adapter, rest, ok := strings.Cut(value, ":")
	identity, revision, hasRevision := strings.Cut(rest, "@")
	return ok && hasRevision && adapter != "" && identity != "" && revision != ""
}
func validOperatorStatus(status knowl.OperationStatus) bool {
	switch status {
	case "", knowl.StatusReceived, knowl.StatusPlanned, knowl.StatusAwaitingReview, knowl.StatusApplying, knowl.StatusCommitted, knowl.StatusFailed:
		return true
	default:
		return false
	}
}
