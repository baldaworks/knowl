package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

var (
	ErrQueryInvalid      = errors.New("invalid Knowl query")
	ErrPageNotFound      = errors.New("knowl page not found")
	ErrOperationNotFound = errors.New("knowl operation not found")
	ErrFilingInvalid     = errors.New("invalid explicit wiki filing")
)

const maxSourcesFilter = 16

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
	Scope     knowl.ScopeRef        `json:"scope"`
	Query     string                `json:"query"`
	Pages     []knowl.PageReference `json:"pages"`
	Links     []knowl.LinkReference `json:"links"`
	Citations []Citation            `json:"citations"`
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

// Search returns bounded, untrusted page references from the projection.
func (service *QueryService) Search(ctx context.Context, scope knowl.ScopeRef, query string, limits knowl.ReadLimits, sources []knowl.SourceID) ([]knowl.PageReference, error) {
	ctx = nonNilContext(ctx)
	if strings.TrimSpace(string(scope)) == "" || strings.TrimSpace(query) == "" {
		return nil, ErrQueryInvalid
	}
	readLimits, err := service.limitsFor(limits)
	if err != nil {
		return nil, err
	}
	normalizedSources, err := NormalizeSourcesFilter(sources)
	if err != nil {
		return nil, err
	}
	readCtx, cancel := boundedReadContext(ctx, readLimits)
	defer cancel()
	references, err := service.index.Search(readCtx, scope, strings.TrimSpace(query), readLimits, normalizedSources)
	if err != nil {
		return nil, fmt.Errorf("search wiki: %w", err)
	}
	if len(references) > readLimits.Pages {
		references = references[:readLimits.Pages]
	}
	for index := range references {
		references[index].Untrusted = true
		references[index].SourceRefs = append([]string(nil), references[index].SourceRefs...)
		if references[index].SourceDocument != nil {
			document := *references[index].SourceDocument
			references[index].SourceDocument = &document
		}
	}
	return references, nil
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
	pages, err := service.Search(readCtx, scope, query, readLimits, sources)
	if err != nil {
		return QueryResult{}, err
	}
	result := QueryResult{Scope: scope, Query: strings.TrimSpace(query), Pages: pages, Links: make([]knowl.LinkReference, 0), Citations: make([]Citation, 0)}
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
