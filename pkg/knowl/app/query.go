package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	"github.com/baldaworks/knowl/pkg/knowl/types"
)

var (
	ErrQueryInvalid       = errors.New("invalid Knowl query")
	ErrPageNotFound       = errors.New("knowl page not found")
	ErrPageResultTooLarge = errors.New("knowl page result exceeds limit")
	ErrPageResultEncoding = errors.New("knowl page result encoding failed")
	ErrOperationNotFound  = errors.New("knowl operation not found")
	ErrFilingInvalid      = errors.New("invalid explicit wiki filing")
)

const (
	maxSourcesFilter      = 16
	MaxGetPageResultBytes = 4 << 20
)

// QueryOptions configures bounded wiki-first reads.
type QueryOptions struct {
	ReadLimits knowl.ReadLimits
}

// Citation is an untrusted wiki or raw-source reference assembled from query results.
type Citation struct {
	Kind      string       `json:"kind"`
	Reference string       `json:"reference"`
	PageID    knowl.PageID `json:"page_id,omitempty"`
	Path      string       `json:"path,omitempty"`
	SourceRef string       `json:"source_ref,omitempty"`
	Untrusted bool         `json:"untrusted"`
}

// QueryResult is a bounded, read-only composition of wiki references and raw citations.
type QueryResult struct {
	Retrieval *knowl.RetrievalReport `json:"retrieval,omitempty"`
	Scope     knowl.ScopeRef         `json:"scope"`
	Query     string                 `json:"query"`
	Pages     []knowl.PageReference  `json:"pages"`
	Links     []knowl.LinkReference  `json:"links"`
	Citations []Citation             `json:"citations"`
}

// GetPageRequest identifies one canonical page in a resolved scope.
type GetPageRequest struct {
	Scope  knowl.ScopeRef `json:"scope"`
	PageID knowl.PageID   `json:"page_id"`
}

// PageResult is the detached public representation of one canonical page.
// Markdown is the sole complete page-content field.
type PageResult struct {
	PageID          knowl.PageID           `json:"page_id"`
	Title           string                 `json:"title"`
	Markdown        string                 `json:"markdown"`
	Digest          string                 `json:"digest"`
	Version         string                 `json:"version"`
	WikiPaths       []string               `json:"wiki_paths,omitempty"`
	OKF             *okf.Metadata          `json:"okf,omitempty"`
	SourceRefs      []string               `json:"source_refs,omitempty"`
	SourceDocuments []knowl.SourceDocument `json:"source_documents,omitempty"`
	UpdatedAt       time.Time              `json:"updated_at"`
	Untrusted       bool                   `json:"untrusted"`
}

// GetPageResult contains one complete page and its bounded direct links.
// Version identifies the page content, not an atomic graph snapshot.
type GetPageResult struct {
	Scope   knowl.ScopeRef        `json:"scope"`
	Page    PageResult            `json:"page"`
	Links   []knowl.LinkReference `json:"links"`
	Version string                `json:"version"`
}

// FilingRequest explicitly submits a query result and typed edit plan to the normal filing gate.
type FilingRequest struct {
	Query  string              `json:"query"`
	Result QueryResult         `json:"result"`
	Plan   knowl.ModelEditPlan `json:"plan"`
}

// QueryService owns bounded wiki-first reads and explicit filing.
type QueryService struct {
	content    ContentStore
	operations OperationStore
	index      SearchIndex
	filer      *IngestService
	limits     knowl.ReadLimits
}

// NewQueryService constructs a read service. Filing is available when filer is non-nil.
func NewQueryService(content ContentStore, operations OperationStore, index SearchIndex, filer *IngestService, options QueryOptions) (*QueryService, error) {
	if content == nil || operations == nil || index == nil {
		return nil, fmt.Errorf("query dependencies are required")
	}
	limits, err := normalizeReadLimits(options.ReadLimits)
	if err != nil {
		return nil, err
	}
	return &QueryService{content: content, operations: operations, index: index, filer: filer, limits: limits}, nil
}

// Page returns one bounded, untrusted canonical page read.
func (service *QueryService) Page(ctx context.Context, scope knowl.ScopeRef, id knowl.PageID, limits knowl.ReadLimits) (knowl.PageSnapshot, error) {
	ctx = nonNilContext(ctx)
	if strings.TrimSpace(string(scope)) == "" || strings.TrimSpace(string(id)) == "" {
		return knowl.PageSnapshot{}, ErrQueryInvalid
	}
	if reservedOKFPage(id) {
		return knowl.PageSnapshot{}, fmt.Errorf("%s: %w", id, ErrPageNotFound)
	}
	readLimits, err := service.limitsFor(limits)
	if err != nil {
		return knowl.PageSnapshot{}, err
	}
	readCtx, cancel := boundedReadContext(ctx, readLimits)
	defer cancel()
	pages, err := service.content.ReadPages(readCtx, scope, []knowl.PageID{id}, readLimits)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return knowl.PageSnapshot{}, fmt.Errorf("%s: %w", id, ErrPageNotFound)
		}
		return knowl.PageSnapshot{}, fmt.Errorf("read page: %w", err)
	}
	if len(pages) == 0 {
		return knowl.PageSnapshot{}, fmt.Errorf("%s: %w", id, ErrPageNotFound)
	}
	page := pages[0]
	page.Untrusted = true
	return page, nil
}

// GetPage returns one complete detached page and its direct navigation context.
func (service *QueryService) GetPage(ctx context.Context, request GetPageRequest) (GetPageResult, error) {
	ctx = nonNilContext(ctx)
	if strings.TrimSpace(string(request.Scope)) == "" || !validRetirementPageID(request.PageID) {
		return GetPageResult{}, ErrQueryInvalid
	}
	readCtx, cancel := boundedReadContext(ctx, service.limits)
	defer cancel()
	page, err := service.Page(readCtx, request.Scope, request.PageID, service.limits)
	if err != nil {
		return GetPageResult{}, err
	}
	if page.ID != request.PageID {
		return GetPageResult{}, fmt.Errorf("page identity mismatch: %w", ErrOperatorWorkspaceUnavailable)
	}
	links, err := service.Links(readCtx, request.Scope, request.PageID, service.limits)
	if err != nil {
		return GetPageResult{}, err
	}
	metadata, err := detachedOKFMetadata(page.OKF)
	if err != nil {
		return GetPageResult{}, err
	}
	result := GetPageResult{
		Scope: request.Scope,
		Page: PageResult{
			PageID: request.PageID, Title: page.Title, Markdown: page.Content,
			Digest: page.Digest, Version: page.Digest, WikiPaths: append([]string(nil), page.WikiPaths...),
			OKF: metadata, SourceRefs: append([]string(nil), page.SourceRefs...),
			SourceDocuments: sortedSourceDocuments(page.SourceDocuments), UpdatedAt: page.UpdatedAt, Untrusted: true,
		},
		Links: append([]knowl.LinkReference(nil), links...), Version: page.Digest,
	}
	if page.SourceDocument != nil && len(result.Page.SourceDocuments) == 0 {
		result.Page.SourceDocuments = []knowl.SourceDocument{*page.SourceDocument}
	}
	for index := range result.Links {
		result.Links[index].Untrusted = true
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return GetPageResult{}, fmt.Errorf("encode GetPage result: %w: %w", err, ErrPageResultEncoding)
	}
	if len(encoded) > MaxGetPageResultBytes {
		return GetPageResult{}, fmt.Errorf("GetPage result bytes %d exceed %d: %w", len(encoded), MaxGetPageResultBytes, ErrPageResultTooLarge)
	}
	return result, nil
}

func detachedOKFMetadata(metadata *okf.Metadata) (*okf.Metadata, error) {
	if metadata == nil {
		return nil, nil
	}
	result := *metadata
	result.Tags = append([]string(nil), metadata.Tags...)
	result.Sources = append([]okf.Source(nil), metadata.Sources...)
	for index := range result.Sources {
		result.Sources[index].UsageCount = cloneUint64(metadata.Sources[index].UsageCount)
		result.Sources[index].LastModified = cloneTime(metadata.Sources[index].LastModified)
		result.Sources[index].Extensions = cloneMetadataExtensions(metadata.Sources[index].Extensions)
		result.Sources[index].UsageWindow = cloneUsageWindow(metadata.Sources[index].UsageWindow)
	}
	result.UsageWindow = cloneUsageWindow(metadata.UsageWindow)
	if metadata.Generated != nil {
		generated := *metadata.Generated
		generated.At = cloneTime(metadata.Generated.At)
		generated.Extensions = cloneMetadataExtensions(metadata.Generated.Extensions)
		result.Generated = &generated
	}
	result.Verified = append([]okf.Verification(nil), metadata.Verified...)
	for index := range result.Verified {
		result.Verified[index].Extensions = cloneMetadataExtensions(metadata.Verified[index].Extensions)
	}
	if metadata.StaleAfter != nil {
		staleAfter := *metadata.StaleAfter
		result.StaleAfter = &staleAfter
	}
	result.Parameters = append([]okf.Parameter(nil), metadata.Parameters...)
	for index := range result.Parameters {
		result.Parameters[index].Extensions = cloneMetadataExtensions(metadata.Parameters[index].Extensions)
	}
	if metadata.Executor != nil {
		executor := *metadata.Executor
		executor.Receipt = append([]string(nil), metadata.Executor.Receipt...)
		executor.Extensions = cloneMetadataExtensions(metadata.Executor.Extensions)
		result.Executor = &executor
	}
	if metadata.Attester != nil {
		attester := *metadata.Attester
		attester.Extensions = cloneMetadataExtensions(metadata.Attester.Extensions)
		result.Attester = &attester
	}
	result.Extensions = cloneMetadataExtensions(metadata.Extensions)
	return &result, nil
}

func cloneUint64(input *uint64) *uint64 {
	if input == nil {
		return nil
	}
	result := *input
	return &result
}

func cloneTime(input *time.Time) *time.Time {
	if input == nil {
		return nil
	}
	result := *input
	return &result
}

func cloneUsageWindow(input *okf.UsageWindow) *okf.UsageWindow {
	if input == nil {
		return nil
	}
	result := *input
	result.Extensions = cloneMetadataExtensions(input.Extensions)
	return &result
}

func cloneMetadataExtensions(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = cloneMetadataValue(value)
	}
	return result
}

func cloneMetadataValue(input any) any {
	switch value := input.(type) {
	case map[string]any:
		return cloneMetadataExtensions(value)
	case []any:
		result := make([]any, len(value))
		for index := range value {
			result[index] = cloneMetadataValue(value[index])
		}
		return result
	default:
		return value
	}
}

func reservedOKFPage(id knowl.PageID) bool {
	base := path.Base(strings.TrimSuffix(string(id), ".md"))
	return base == "index" || base == "log"
}

// Search returns bounded, untrusted page references from the projection.
func (service *QueryService) Search(ctx context.Context, scope knowl.ScopeRef, query string, limits knowl.ReadLimits, sources []knowl.SourceID) ([]knowl.PageReference, error) {
	refs, _, err := service.searchWithReport(ctx, scope, query, limits, sources)
	return refs, err
}

func (service *QueryService) searchWithReport(ctx context.Context, scope knowl.ScopeRef, query string, limits knowl.ReadLimits, sources []knowl.SourceID) ([]knowl.PageReference, *knowl.RetrievalReport, error) {
	ctx = nonNilContext(ctx)
	if strings.TrimSpace(string(scope)) == "" || strings.TrimSpace(query) == "" {
		return nil, nil, ErrQueryInvalid
	}
	readLimits, err := service.limitsFor(limits)
	if err != nil {
		return nil, nil, err
	}
	normalizedSources, err := NormalizeSourcesFilter(sources)
	if err != nil {
		return nil, nil, err
	}
	readCtx, cancel := boundedReadContext(ctx, readLimits)
	defer cancel()
	var report *knowl.RetrievalReport
	var references []knowl.PageReference
	if reported, ok := service.index.(ReportedSearchIndex); ok {
		var value knowl.RetrievalReport
		references, value, err = reported.SearchWithReport(readCtx, scope, strings.TrimSpace(query), readLimits, normalizedSources)
		if reportErr := ValidateRetrievalReport(value); reportErr != nil {
			return nil, nil, reportErr
		}
		report = &value
	} else {
		references, err = service.index.Search(readCtx, scope, strings.TrimSpace(query), readLimits, normalizedSources)
	}
	if err != nil {
		return nil, report, fmt.Errorf("search wiki: %w", err)
	}
	if len(references) > readLimits.Pages {
		references = references[:readLimits.Pages]
	}
	for index := range references {
		references[index].Untrusted = true
		references[index].SourceRefs = append([]string(nil), references[index].SourceRefs...)
		references[index].SourceDocuments = sortedSourceDocuments(references[index].SourceDocuments)
		if references[index].SourceDocument != nil {
			document := *references[index].SourceDocument
			references[index].SourceDocument = &document
			if len(references[index].SourceDocuments) == 0 {
				references[index].SourceDocuments = []knowl.SourceDocument{document}
			}
		} else if len(references[index].SourceDocuments) > 0 {
			document := references[index].SourceDocuments[0]
			references[index].SourceDocument = &document
		}
	}
	return references, report, nil
}

func sortedSourceDocuments(documents []knowl.SourceDocument) []knowl.SourceDocument {
	result := append([]knowl.SourceDocument(nil), documents...)
	sort.Slice(result, func(left, right int) bool {
		if result[left].SourceID != result[right].SourceID {
			return result[left].SourceID < result[right].SourceID
		}
		if result[left].DocumentID != result[right].DocumentID {
			return result[left].DocumentID < result[right].DocumentID
		}
		if result[left].Revision != result[right].Revision {
			return result[left].Revision < result[right].Revision
		}
		return result[left].URI < result[right].URI
	})
	unique := result[:0]
	for _, document := range result {
		if len(unique) > 0 && unique[len(unique)-1] == document {
			continue
		}
		unique = append(unique, document)
	}
	return unique
}

// Links returns bounded, untrusted graph references for one page.
func (service *QueryService) Links(ctx context.Context, scope knowl.ScopeRef, page knowl.PageID, limits knowl.ReadLimits) ([]knowl.LinkReference, error) {
	ctx = nonNilContext(ctx)
	if strings.TrimSpace(string(scope)) == "" || strings.TrimSpace(string(page)) == "" {
		return nil, ErrQueryInvalid
	}
	readLimits, err := service.limitsFor(limits)
	if err != nil {
		return nil, err
	}
	readCtx, cancel := boundedReadContext(ctx, readLimits)
	defer cancel()
	links, err := service.index.Links(readCtx, scope, page, readLimits)
	if err != nil {
		return nil, fmt.Errorf("read wiki links: %w", err)
	}
	if len(links) > readLimits.Pages {
		links = links[:readLimits.Pages]
	}
	for index := range links {
		links[index].Untrusted = true
	}
	return links, nil
}

// Operation returns the redacted durable status for one scoped operation.
func (service *QueryService) Operation(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID) (knowl.Operation, error) {
	ctx = nonNilContext(ctx)
	if strings.TrimSpace(string(scope)) == "" || strings.TrimSpace(string(id)) == "" {
		return knowl.Operation{}, ErrQueryInvalid
	}
	readCtx, cancel := boundedReadContext(ctx, service.limits)
	defer cancel()
	return service.operations.Operation(readCtx, scope, id)
}

// Query assembles page references, link context, and page/raw citations without mutating state.
func (service *QueryService) Query(ctx context.Context, scope knowl.ScopeRef, query string, limits knowl.ReadLimits, sources []knowl.SourceID) (QueryResult, error) {
	ctx = nonNilContext(ctx)
	readLimits, err := service.limitsFor(limits)
	if err != nil {
		return QueryResult{}, err
	}
	readCtx, cancel := boundedReadContext(ctx, readLimits)
	defer cancel()
	pages, report, err := service.searchWithReport(readCtx, scope, query, readLimits, sources)
	if err != nil {
		return QueryResult{Retrieval: report}, err
	}
	result := QueryResult{Retrieval: report, Scope: scope, Query: strings.TrimSpace(query), Pages: pages, Links: make([]knowl.LinkReference, 0), Citations: make([]Citation, 0)}
	seenLinks := make(map[string]struct{})
	seenCitations := make(map[string]struct{})
	for _, page := range pages {
		wikiCitation := Citation{Kind: "wiki", Reference: string(page.ID), PageID: page.ID, Path: page.Path, Untrusted: true}
		appendCitation(&result, seenCitations, wikiCitation)
		for _, sourceRef := range page.SourceRefs {
			appendCitation(&result, seenCitations, Citation{Kind: "raw", Reference: sourceRef, PageID: page.ID, Path: page.Path, SourceRef: sourceRef, Untrusted: true})
		}
		links, linkErr := service.Links(readCtx, scope, page.ID, readLimits)
		if linkErr != nil {
			return QueryResult{}, linkErr
		}
		for _, link := range links {
			key := string(link.From) + "\x00" + string(link.To) + "\x00" + link.Relation
			if _, exists := seenLinks[key]; exists {
				continue
			}
			seenLinks[key] = struct{}{}
			result.Links = append(result.Links, link)
		}
	}
	sort.Slice(result.Links, func(left, right int) bool {
		if result.Links[left].From == result.Links[right].From {
			if result.Links[left].To == result.Links[right].To {
				return result.Links[left].Relation < result.Links[right].Relation
			}
			return result.Links[left].To < result.Links[right].To
		}
		return result.Links[left].From < result.Links[right].From
	})
	if len(result.Links) > readLimits.Pages {
		result.Links = result.Links[:readLimits.Pages]
	}
	sort.Slice(result.Citations, func(left, right int) bool {
		if result.Citations[left].Kind == result.Citations[right].Kind {
			if result.Citations[left].Reference == result.Citations[right].Reference {
				return result.Citations[left].PageID < result.Citations[right].PageID
			}
			return result.Citations[left].Reference < result.Citations[right].Reference
		}
		return result.Citations[left].Kind < result.Citations[right].Kind
	})
	return result, nil
}

// NormalizeSourcesFilter returns a canonical bounded source identity filter.
// Empty input means unfiltered retrieval.
func NormalizeSourcesFilter(sources []knowl.SourceID) ([]knowl.SourceID, error) {
	if len(sources) > 256 {
		return nil, ErrSourceInvalid
	}
	normalized := make([]knowl.SourceID, 0, len(sources))
	for _, source := range sources {
		trimmed := knowl.SourceID(strings.TrimSpace(string(source)))
		if trimmed == "" {
			continue
		}
		if err := ValidateSourceID(trimmed); err != nil {
			return nil, ErrSourceInvalid
		}
		normalized = append(normalized, trimmed)
	}
	sort.Slice(normalized, func(left, right int) bool { return normalized[left] < normalized[right] })
	unique := normalized[:0]
	for _, source := range normalized {
		if len(unique) == 0 || unique[len(unique)-1] != source {
			unique = append(unique, source)
		}
	}
	if len(unique) > maxSourcesFilter {
		return nil, ErrSourceInvalid
	}
	return unique, nil
}

// File explicitly files a query result through the standard immutable-source and plan/apply workflow.
func (service *QueryService) File(ctx context.Context, scope knowl.ScopeRef, request FilingRequest) (IngestResult, error) {
	ctx = nonNilContext(ctx)
	if service.filer == nil {
		return IngestResult{}, fmt.Errorf("query filing is unavailable: %w", ErrFilingInvalid)
	}
	if strings.TrimSpace(string(scope)) == "" || strings.TrimSpace(request.Query) == "" || request.Result.Scope != scope || strings.TrimSpace(request.Result.Query) != strings.TrimSpace(request.Query) || len(request.Plan.Edits) == 0 {
		return IngestResult{}, ErrFilingInvalid
	}
	payload, err := json.Marshal(struct {
		Query  string      `json:"query"`
		Result QueryResult `json:"result"`
	}{Query: strings.TrimSpace(request.Query), Result: request.Result})
	if err != nil {
		return IngestResult{}, fmt.Errorf("encode query filing source: %w", err)
	}
	digest := sha256.Sum256(payload)
	digestText := hex.EncodeToString(digest[:])
	envelope := knowl.SourceEnvelope{
		Scope:     scope,
		Source:    knowl.SourceRef{Adapter: "query", ID: "result-" + digestText[:16]},
		Version:   knowl.SourceVersion{Version: "1", Digest: digestText},
		MediaType: "application/json",
		Content:   payload,
	}
	accepted := knowl.AcceptedSource{Scope: scope, Source: envelope.Source, Version: envelope.Version, MediaType: envelope.MediaType}
	plan := request.Plan
	plan.SourceRefs = append([]string(nil), plan.SourceRefs...)
	filingRef := SourceRefKey(accepted)
	for _, sourceRef := range plan.SourceRefs {
		if sourceRef == filingRef {
			return service.filer.FilePlan(ctx, envelope, plan)
		}
	}
	plan.SourceRefs = append(plan.SourceRefs, filingRef)
	sort.Strings(plan.SourceRefs)
	return service.filer.FilePlan(ctx, envelope, plan)
}

func (service *QueryService) limitsFor(limits knowl.ReadLimits) (knowl.ReadLimits, error) {
	if limits == (knowl.ReadLimits{}) {
		return service.limits, nil
	}
	return normalizeReadLimits(limits)
}

func normalizeReadLimits(limits knowl.ReadLimits) (knowl.ReadLimits, error) {
	if limits == (knowl.ReadLimits{}) {
		return DefaultReadLimits(), nil
	}
	defaults := DefaultReadLimits()
	if limits.Pages < 0 || limits.Bytes < 0 || limits.Characters < 0 || limits.Depth < 0 || limits.Deadline < 0 {
		return knowl.ReadLimits{}, fmt.Errorf("invalid query read limits: %w", ErrQueryInvalid)
	}
	if limits.Pages == 0 {
		limits.Pages = defaults.Pages
	}
	if limits.Bytes == 0 {
		limits.Bytes = defaults.Bytes
	}
	if limits.Characters == 0 {
		limits.Characters = defaults.Characters
	}
	if limits.Depth == 0 {
		limits.Depth = defaults.Depth
	}
	if limits.Deadline == 0 {
		limits.Deadline = defaults.Deadline
	}
	return limits, nil
}

func appendCitation(result *QueryResult, seen map[string]struct{}, citation Citation) {
	key := citation.Kind + "\x00" + citation.Reference + "\x00" + string(citation.PageID)
	if _, exists := seen[key]; exists {
		return
	}
	seen[key] = struct{}{}
	result.Citations = append(result.Citations, citation)
}

func boundedReadContext(ctx context.Context, limits knowl.ReadLimits) (context.Context, context.CancelFunc) {
	if limits.Deadline <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, limits.Deadline)
}
