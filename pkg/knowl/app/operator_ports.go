package app

import (
	"context"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// OperatorContinuation is a verified backend position. Canonical readers bind
// Key to SnapshotVersion. Operation readers use an immutable created_at +
// operation_id tuple; mutable status/updated_at values cannot be positions.
// Readers never receive or decode an untrusted public cursor.
type OperatorContinuation struct {
	Key             string
	SnapshotVersion string
}

// OperatorReadOptions bounds one reader call under a finite read deadline.
type OperatorReadOptions struct {
	Limit        int
	Continuation OperatorContinuation
	ReadLimits   knowl.ReadLimits
}

// OperatorReadPage is a backend result, not a public cursor envelope. Canonical
// readers supply a SHA-256 SnapshotVersion even for a successful empty list.
// NextKey is immutable, bounded, and empty when there is no next page.
type OperatorReadPage[T any] struct {
	Items           []T
	NextKey         string
	SnapshotVersion string
}

// OperatorCatalogRead contains one parent and its bounded direct child page.
type OperatorCatalogRead struct {
	Parent   knowl.OperatorCatalogSummary
	Children OperatorReadPage[knowl.OperatorCatalogChild]
}

// CatalogReader is an optional consistent canonical catalog capability.
type CatalogReader interface {
	CatalogChildren(ctx context.Context, scope knowl.ScopeRef, parent knowl.PageID, options OperatorReadOptions) (OperatorCatalogRead, error)
}

// PageSummaryReader is an optional consistent factual-page inventory capability.
type PageSummaryReader interface {
	PageSummaries(ctx context.Context, scope knowl.ScopeRef, options OperatorReadOptions) (OperatorReadPage[knowl.OperatorPageSummary], error)
}

// PageReader is an optional bounded current canonical page capability.
type PageReader interface {
	Page(ctx context.Context, scope knowl.ScopeRef, id knowl.PageID, limits knowl.ReadLimits) (knowl.OperatorPage, error)
}

// SourceRevisionReader is an optional bounded immutable accepted-text capability.
type SourceRevisionReader interface {
	SourceRevision(ctx context.Context, scope knowl.ScopeRef, ref string, limits knowl.ReadLimits) (knowl.OperatorSourceRevision, error)
}

// WorkspaceReader composes optional canonical capabilities without extending
// ContentStore. A host may also provide each capability independently.
type WorkspaceReader interface {
	CatalogReader
	PageSummaryReader
	PageReader
	SourceRevisionReader
}

// OperatorOperationReadOptions adds normalized stored-association filters.
type OperatorOperationReadOptions struct {
	OperatorReadOptions
	Status   knowl.OperationStatus
	SourceID knowl.SourceID
}

// OperationLister is an optional scoped, filterable durable read capability.
// Continuation keys must describe the immutable operation creation tuple.
type OperationLister interface {
	ListOperations(ctx context.Context, scope knowl.ScopeRef, options OperatorOperationReadOptions) (OperatorReadPage[knowl.OperatorOperationSummary], error)
}

// SourceDocumentLister is an optional scoped durable document read capability.
type SourceDocumentLister interface {
	ListSourceDocuments(ctx context.Context, scope knowl.ScopeRef, id knowl.SourceID, options OperatorReadOptions) (OperatorReadPage[knowl.OperatorDocumentSummary], error)
}

// OperatorSourceReader composes configured source identities and durable status.
// It must return safe detached projections, never source configuration.
type OperatorSourceReader interface {
	ListSources(ctx context.Context, scope knowl.ScopeRef, options OperatorReadOptions) (OperatorReadPage[knowl.OperatorSourceSummary], error)
	Source(ctx context.Context, scope knowl.ScopeRef, id knowl.SourceID) (knowl.OperatorSourceSummary, error)
}

// OperatorReaders contains only independently optional read capabilities.
type OperatorReaders struct {
	Catalogs   CatalogReader
	Pages      PageSummaryReader
	Page       PageReader
	Revisions  SourceRevisionReader
	Sources    OperatorSourceReader
	Documents  SourceDocumentLister
	Operations OperationLister
}
