package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	operatorTestScope       = "host-scope"
	operatorTestPageID      = "concept"
	operatorTestOperationID = "operation"
	operatorTestSourceID    = "source"
	operatorSnapshot        = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

type operatorPageFixture struct {
	result  OperatorReadPage[knowl.OperatorPageSummary]
	err     error
	scope   knowl.ScopeRef
	options OperatorReadOptions
	calls   int
}

func (reader *operatorPageFixture) PageSummaries(_ context.Context, scope knowl.ScopeRef, options OperatorReadOptions) (OperatorReadPage[knowl.OperatorPageSummary], error) {
	reader.scope, reader.options = scope, options
	reader.calls++
	return reader.result, reader.err
}
func operatorService(t *testing.T, readers OperatorReaders) *OperatorService {
	t.Helper()
	service, err := NewOperatorService(operatorTestScope, readers, OperatorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

// Catches loss of bounded defaults, host scope binding, and empty-list identity.
func TestOperatorPaginationDefaultsAndBounds(t *testing.T) {
	for _, test := range []struct {
		limit, want int
		invalid     bool
	}{{0, 50, false}, {1, 1, false}, {100, 100, false}, {-1, 0, true}, {101, 0, true}} {
		t.Run(strconv.Itoa(test.limit), func(t *testing.T) {
			reader := &operatorPageFixture{result: OperatorReadPage[knowl.OperatorPageSummary]{SnapshotVersion: operatorSnapshot}}
			result, err := operatorService(t, OperatorReaders{Pages: reader}).PageSummaries(t.Context(), OperatorListOptions{Limit: test.limit})
			if test.invalid {
				if !errors.Is(err, ErrOperatorLimitInvalid) || reader.calls != 0 {
					t.Fatalf("err=%v calls=%d", err, reader.calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if reader.scope != operatorTestScope || reader.options.Limit != test.want || result.Items == nil || result.SnapshotVersion != operatorSnapshot {
				t.Fatalf("result=%+v scope=%q options=%+v", result, reader.scope, reader.options)
			}
		})
	}
}

// Catches unchecked cursor signatures, versions, endpoint/scope/filter/limit binding,
// restart replay, and omission of the snapshot/continuation handed to the reader.
func TestOperatorCursorBinding(t *testing.T) {
	reader := &operatorPageFixture{result: OperatorReadPage[knowl.OperatorPageSummary]{SnapshotVersion: operatorSnapshot, NextKey: "immutable-page-key"}}
	service := operatorService(t, OperatorReaders{Pages: reader})
	first, err := service.PageSummaries(t.Context(), OperatorListOptions{})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("result=%+v err=%v", first, err)
	}
	_, err = service.PageSummaries(t.Context(), OperatorListOptions{Limit: 50, Cursor: first.NextCursor})
	if err != nil || reader.options.Continuation.Key != "immutable-page-key" || reader.options.Continuation.SnapshotVersion != operatorSnapshot {
		t.Fatalf("options=%+v err=%v", reader.options, err)
	}
	cases := []struct {
		name    string
		service *OperatorService
		options OperatorListOptions
	}{
		{"different limit", service, OperatorListOptions{Limit: 1, Cursor: first.NextCursor}},
		{"restart", operatorService(t, OperatorReaders{Pages: reader}), OperatorListOptions{Cursor: first.NextCursor}},
		{"malformed", service, OperatorListOptions{Cursor: "!"}},
		{"oversized", service, OperatorListOptions{Cursor: strings.Repeat("a", 8193)}},
		{"signature", service, OperatorListOptions{Cursor: alterOperatorCursor(t, service, first.NextCursor, false, func(wire *operatorCursorEnvelope) { wire.Payload.Key = "forged" })}},
		{"cursor version", service, OperatorListOptions{Cursor: alterOperatorCursor(t, service, first.NextCursor, true, func(wire *operatorCursorEnvelope) { wire.Payload.Version = 2 })}},
		{"endpoint", service, OperatorListOptions{Cursor: alterOperatorCursor(t, service, first.NextCursor, true, func(wire *operatorCursorEnvelope) { wire.Payload.Endpoint = operatorOperationsEndpoint })}},
		{"scope", service, OperatorListOptions{Cursor: alterOperatorCursor(t, service, first.NextCursor, true, func(wire *operatorCursorEnvelope) { wire.Payload.Scope = "another-scope" })}},
		{"filter", service, OperatorListOptions{Cursor: alterOperatorCursor(t, service, first.NextCursor, true, func(wire *operatorCursorEnvelope) { wire.Payload.Filter = "another-filter" })}},

		{"missing snapshot", service, OperatorListOptions{Cursor: alterOperatorCursor(t, service, first.NextCursor, true, func(wire *operatorCursorEnvelope) { wire.Payload.SnapshotVersion = "" })}},
		{"missing key", service, OperatorListOptions{Cursor: alterOperatorCursor(t, service, first.NextCursor, true, func(wire *operatorCursorEnvelope) { wire.Payload.Key = "" })}},
		{"oversized key", service, OperatorListOptions{Cursor: alterOperatorCursor(t, service, first.NextCursor, true, func(wire *operatorCursorEnvelope) { wire.Payload.Key = strings.Repeat("a", 4097) })}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			before := reader.calls
			_, err := test.service.PageSummaries(t.Context(), test.options)
			if !errors.Is(err, ErrOperatorCursorInvalid) || reader.calls != before {
				t.Fatalf("err=%v calls=%d", err, reader.calls)
			}
		})
	}
	reader.result.SnapshotVersion = strings.Repeat("b", 64)
	_, err = service.PageSummaries(t.Context(), OperatorListOptions{Cursor: first.NextCursor})
	if !errors.Is(err, ErrOperatorSnapshotChanged) {
		t.Fatalf("err=%v", err)
	}
}

func alterOperatorCursor(t *testing.T, service *OperatorService, cursor string, sign bool, alter func(*operatorCursorEnvelope)) string {
	t.Helper()
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatal(err)
	}
	var wire operatorCursorEnvelope
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	alter(&wire)
	if sign {
		payload, err := json.Marshal(wire.Payload)
		if err != nil {
			t.Fatal(err)
		}
		mac := hmac.New(sha256.New, service.cursorKey[:])
		_, _ = mac.Write(payload)
		wire.Signature = base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	data, err = json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

// Catches missing optional readers masquerading as empty success.
func TestOperatorCapabilityAbsence(t *testing.T) {
	service := operatorService(t, OperatorReaders{})
	calls := []func() error{
		func() error { _, err := service.CatalogChildren(t.Context(), "", OperatorListOptions{}); return err },
		func() error { _, err := service.PageSummaries(t.Context(), OperatorListOptions{}); return err },
		func() error { _, err := service.Page(t.Context(), operatorTestPageID); return err },
		func() error { _, err := service.SourceRevision(t.Context(), "file:document@v1"); return err },
		func() error { _, err := service.Sources(t.Context(), OperatorListOptions{}); return err },
		func() error {
			_, err := service.Source(t.Context(), operatorTestSourceID, OperatorListOptions{})
			return err
		},
		func() error { _, err := service.Operations(t.Context(), OperatorOperationListOptions{}); return err },
	}
	for index, call := range calls {
		if err := call(); !errors.Is(err, ErrOperatorCapabilityUnavailable) {
			t.Fatalf("call %d: %v", index, err)
		}
	}
}

type operatorSingleFixture struct {
	page     knowl.OperatorPage
	revision knowl.OperatorSourceRevision
	err      error
	scope    knowl.ScopeRef
	limits   knowl.ReadLimits
}

func (reader *operatorSingleFixture) Page(_ context.Context, scope knowl.ScopeRef, _ knowl.PageID, limits knowl.ReadLimits) (knowl.OperatorPage, error) {
	reader.scope, reader.limits = scope, limits
	return reader.page, reader.err
}
func (reader *operatorSingleFixture) SourceRevision(_ context.Context, scope knowl.ScopeRef, _ string, limits knowl.ReadLimits) (knowl.OperatorSourceRevision, error) {
	reader.scope, reader.limits = scope, limits
	return reader.revision, reader.err
}

// Catches missing/invalid/resource-limit/unavailable conflation and mutable DTO sharing.
func TestOperatorSingleReads(t *testing.T) {
	reader := &operatorSingleFixture{page: knowl.OperatorPage{ID: operatorTestPageID, RelatedPageIDs: []knowl.PageID{"related"}, Metadata: &knowl.OperatorPageMetadata{Tags: []string{"tag"}}}, revision: knowl.OperatorSourceRevision{SourceRef: "file:document@v1", Text: "immutable"}}
	service := operatorService(t, OperatorReaders{Page: reader, Revisions: reader})
	page, err := service.Page(t.Context(), operatorTestPageID)
	if err != nil {
		t.Fatal(err)
	}
	page.RelatedPageIDs[0] = "changed"
	page.Metadata.Tags[0] = "changed"
	if reader.page.RelatedPageIDs[0] != "related" || reader.page.Metadata.Tags[0] != "tag" || reader.scope != operatorTestScope || reader.limits.Bytes <= 0 || reader.limits.Deadline <= 0 {
		t.Fatalf("reader=%+v", reader)
	}
	revision, err := service.SourceRevision(t.Context(), "file:document@v1")
	if err != nil || revision.Text != "immutable" {
		t.Fatalf("revision=%+v err=%v", revision, err)
	}
	for _, id := range []knowl.PageID{"", "../outside", "/absolute", "a\\b", "a\x00b", knowl.PageID(strings.Repeat("a", 2049))} {
		_, err := service.Page(t.Context(), id)
		if !errors.Is(err, ErrOperatorInvalidRequest) {
			t.Fatalf("id=%q err=%v", id, err)
		}
	}
	for _, ref := range []string{"", "no-revision", "file:doc@", "file:doc@v\n1", strings.Repeat("a", 4097)} {
		_, err := service.SourceRevision(t.Context(), ref)
		if !errors.Is(err, ErrOperatorInvalidRequest) {
			t.Fatalf("ref=%q err=%v", ref, err)
		}
	}
	reader.page = knowl.OperatorPage{}
	_, err = service.Page(t.Context(), operatorTestPageID)
	if !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("err=%v", err)
	}
	reader.revision = knowl.OperatorSourceRevision{}
	_, err = service.SourceRevision(t.Context(), "file:document@v1")
	if !errors.Is(err, ErrOperatorSourceRevisionNotFound) {
		t.Fatalf("err=%v", err)
	}
	for _, want := range []error{ErrOperatorWorkspaceUnavailable, ErrOperatorNotReady, ErrOperatorReadLimitExceeded, ErrOperatorUnsupportedFormat, ErrPageNotFound} {
		reader.err = want
		_, err = service.Page(t.Context(), operatorTestPageID)
		if !errors.Is(err, want) {
			t.Fatalf("want=%v err=%v", want, err)
		}
	}
}

type operatorOperationsFixture struct {
	options OperatorOperationReadOptions
	scope   knowl.ScopeRef
}

func (reader *operatorOperationsFixture) ListOperations(_ context.Context, scope knowl.ScopeRef, options OperatorOperationReadOptions) (OperatorReadPage[knowl.OperatorOperationSummary], error) {
	reader.scope, reader.options = scope, options
	return OperatorReadPage[knowl.OperatorOperationSummary]{Items: []knowl.OperatorOperationSummary{{ID: operatorTestOperationID, SourceID: operatorTestSourceID}}, NextKey: `{"created_at":"2026-10-06T00:00:00Z","operation_id":"operation"}`}, nil
}

// Catches omission of normalized source/status filters from dispatch and cursor binding.
func TestOperatorOperationFilters(t *testing.T) {
	reader := &operatorOperationsFixture{}
	service := operatorService(t, OperatorReaders{Operations: reader})
	options := OperatorOperationListOptions{Status: knowl.StatusFailed, SourceID: operatorTestSourceID}
	first, err := service.Operations(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	if reader.scope != operatorTestScope || reader.options.Status != knowl.StatusFailed || reader.options.SourceID != operatorTestSourceID || first.Items[0].SourceID != operatorTestSourceID {
		t.Fatalf("reader=%+v first=%+v", reader, first)
	}
	options.Cursor = first.NextCursor
	_, err = service.Operations(t.Context(), options)
	if err != nil || reader.options.Continuation.Key == "" {
		t.Fatalf("err=%v options=%+v", err, reader.options)
	}
	options.Status = knowl.StatusCommitted
	_, err = service.Operations(t.Context(), options)
	if !errors.Is(err, ErrOperatorCursorInvalid) {
		t.Fatalf("err=%v", err)
	}
	options.Status = knowl.StatusFailed
	options.SourceID = "another-source"
	_, err = service.Operations(t.Context(), options)
	if !errors.Is(err, ErrOperatorCursorInvalid) {
		t.Fatalf("err=%v", err)
	}
	for _, invalid := range []OperatorOperationListOptions{{Status: "invalid"}, {SourceID: "../source"}, {SourceID: knowl.SourceID(strings.Repeat("a", 65))}} {
		_, err := service.Operations(t.Context(), invalid)
		if !errors.Is(err, ErrOperatorInvalidRequest) {
			t.Fatalf("err=%v", err)
		}
	}
}

// Existing custom content ports compile without implementing operator readers.
func TestOperatorLegacyCompatibility(t *testing.T) {
	var content ContentStore = fakeContentStore{}
	if _, ok := content.(WorkspaceReader); ok {
		t.Fatal("legacy content unexpectedly provides optional workspace reader")
	}
}

// Catches unbounded backend inventories, missing canonical snapshot identity and invalid setup.
func TestOperatorReaderBounds(t *testing.T) {
	reader := &operatorPageFixture{result: OperatorReadPage[knowl.OperatorPageSummary]{Items: make([]knowl.OperatorPageSummary, 51), SnapshotVersion: operatorSnapshot}}
	service := operatorService(t, OperatorReaders{Pages: reader})
	_, err := service.PageSummaries(t.Context(), OperatorListOptions{})
	if !errors.Is(err, ErrOperatorReadLimitExceeded) {
		t.Fatalf("err=%v", err)
	}
	reader.result = OperatorReadPage[knowl.OperatorPageSummary]{}
	_, err = service.PageSummaries(t.Context(), OperatorListOptions{})
	if !errors.Is(err, ErrOperatorWorkspaceUnavailable) {
		t.Fatalf("err=%v", err)
	}
	_, err = NewOperatorService("", OperatorReaders{}, OperatorOptions{})
	if !errors.Is(err, ErrOperatorInvalidRequest) {
		t.Fatalf("err=%v", err)
	}
	_, err = NewOperatorService("scope", OperatorReaders{}, OperatorOptions{ReadLimits: knowl.ReadLimits{Bytes: -1}})
	if !errors.Is(err, ErrOperatorInvalidRequest) {
		t.Fatalf("err=%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = service.PageSummaries(ctx, OperatorListOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

type operatorCatalogFixture struct {
	scope   knowl.ScopeRef
	parent  knowl.PageID
	options OperatorReadOptions
	result  OperatorCatalogRead
	err     error
}

func (reader *operatorCatalogFixture) CatalogChildren(_ context.Context, scope knowl.ScopeRef, parent knowl.PageID, options OperatorReadOptions) (OperatorCatalogRead, error) {
	reader.scope, reader.parent, reader.options = scope, parent, options
	return reader.result, reader.err
}

type operatorSourcesFixture struct {
	source  knowl.OperatorSourceSummary
	scope   knowl.ScopeRef
	id      knowl.SourceID
	options OperatorReadOptions
	err     error
}

func (reader *operatorSourcesFixture) ListSources(_ context.Context, scope knowl.ScopeRef, options OperatorReadOptions) (OperatorReadPage[knowl.OperatorSourceSummary], error) {
	reader.scope, reader.options = scope, options
	return OperatorReadPage[knowl.OperatorSourceSummary]{Items: []knowl.OperatorSourceSummary{reader.source}, NextKey: "source-key"}, reader.err
}
func (reader *operatorSourcesFixture) Source(_ context.Context, scope knowl.ScopeRef, id knowl.SourceID) (knowl.OperatorSourceSummary, error) {
	reader.scope, reader.id = scope, id
	return reader.source, reader.err
}

type operatorDocumentsFixture struct {
	scope   knowl.ScopeRef
	id      knowl.SourceID
	options OperatorReadOptions
}

func (reader *operatorDocumentsFixture) ListSourceDocuments(_ context.Context, scope knowl.ScopeRef, id knowl.SourceID, options OperatorReadOptions) (OperatorReadPage[knowl.OperatorDocumentSummary], error) {
	reader.scope, reader.id, reader.options = scope, id, options
	return OperatorReadPage[knowl.OperatorDocumentSummary]{Items: []knowl.OperatorDocumentSummary{{ID: "document", MaintenanceOperationID: operatorTestOperationID}}, NextKey: "document-key"}, nil
}

// Catches catalog parent filter replay and missing root/catalog confusion.
func TestOperatorCatalogReads(t *testing.T) {
	reader := &operatorCatalogFixture{result: OperatorCatalogRead{Parent: knowl.OperatorCatalogSummary{ID: "operator-root/index", Title: "Operator root"}, Children: OperatorReadPage[knowl.OperatorCatalogChild]{SnapshotVersion: operatorSnapshot, NextKey: "child", Items: []knowl.OperatorCatalogChild{{ID: operatorTestPageID, Kind: "page"}}}}}
	service := operatorService(t, OperatorReaders{Catalogs: reader})
	first, err := service.CatalogChildren(t.Context(), "", OperatorListOptions{})
	if err != nil || first.Parent.Title != "Operator root" || reader.scope != operatorTestScope || reader.options.Limit != 50 {
		t.Fatalf("first=%+v reader=%+v err=%v", first, reader, err)
	}
	_, err = service.CatalogChildren(t.Context(), "nested/index", OperatorListOptions{Cursor: first.NextCursor})
	if !errors.Is(err, ErrOperatorCursorInvalid) {
		t.Fatalf("err=%v", err)
	}
	_, err = service.CatalogChildren(t.Context(), "../outside", OperatorListOptions{})
	if !errors.Is(err, ErrOperatorInvalidRequest) {
		t.Fatalf("err=%v", err)
	}
	reader.result.Parent = knowl.OperatorCatalogSummary{}
	_, err = service.CatalogChildren(t.Context(), "missing/index", OperatorListOptions{})
	if !errors.Is(err, ErrOperatorCatalogNotFound) {
		t.Fatalf("err=%v", err)
	}
	reader.err = ErrOperatorWorkspaceUnavailable
	_, err = service.CatalogChildren(t.Context(), "", OperatorListOptions{})
	if !errors.Is(err, ErrOperatorWorkspaceUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

// Catches partial-capability fallback, document source cursor replay, and shared status pointers.
func TestOperatorSourceReads(t *testing.T) {
	sources := &operatorSourcesFixture{source: knowl.OperatorSourceSummary{ID: operatorTestSourceID, Type: knowl.SourceTypeGit, Enabled: true, Status: &knowl.OperatorSourceStatus{Status: knowl.SyncStatusSucceeded}}}
	documents := &operatorDocumentsFixture{}
	service := operatorService(t, OperatorReaders{Sources: sources, Documents: documents})
	list, err := service.Sources(t.Context(), OperatorListOptions{})
	if err != nil || len(list.Items) != 1 || sources.scope != operatorTestScope || sources.options.Limit != 50 {
		t.Fatalf("list=%+v sources=%+v err=%v", list, sources, err)
	}
	list.Items[0].Status.Status = knowl.SyncStatusFailed
	if sources.source.Status.Status != knowl.SyncStatusSucceeded {
		t.Fatal("reader status mutated")
	}
	detail, err := service.Source(t.Context(), operatorTestSourceID, OperatorListOptions{})
	if err != nil || detail.Source.ID != operatorTestSourceID || detail.Documents.Items[0].MaintenanceOperationID != operatorTestOperationID || documents.scope != operatorTestScope || documents.id != operatorTestSourceID {
		t.Fatalf("detail=%+v documents=%+v err=%v", detail, documents, err)
	}
	_, err = service.Source(t.Context(), "another-source", OperatorListOptions{Cursor: detail.Documents.NextCursor})
	if !errors.Is(err, ErrOperatorCursorInvalid) {
		t.Fatalf("err=%v", err)
	}
	for _, readers := range []OperatorReaders{{Sources: sources}, {Documents: documents}} {
		_, err := operatorService(t, readers).Source(t.Context(), operatorTestSourceID, OperatorListOptions{})
		if !errors.Is(err, ErrOperatorCapabilityUnavailable) {
			t.Fatalf("err=%v", err)
		}
	}
	_, err = service.Source(t.Context(), "../source", OperatorListOptions{})
	if !errors.Is(err, ErrOperatorInvalidRequest) {
		t.Fatalf("err=%v", err)
	}
	sources.source = knowl.OperatorSourceSummary{}
	_, err = service.Source(t.Context(), "missing", OperatorListOptions{})
	if !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("err=%v", err)
	}
}

// Catches excess page/raw text escaping the host's bounded read policy.
func TestOperatorTextLimits(t *testing.T) {
	reader := &operatorSingleFixture{page: knowl.OperatorPage{ID: operatorTestPageID, Markdown: "four"}, revision: knowl.OperatorSourceRevision{SourceRef: "file:doc@v1", Text: "four"}}
	service, err := NewOperatorService(operatorTestScope, OperatorReaders{Page: reader, Revisions: reader}, OperatorOptions{ReadLimits: knowl.ReadLimits{Bytes: 3}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Page(t.Context(), operatorTestPageID)
	if !errors.Is(err, ErrOperatorReadLimitExceeded) {
		t.Fatalf("err=%v", err)
	}
	_, err = service.SourceRevision(t.Context(), "file:doc@v1")
	if !errors.Is(err, ErrOperatorReadLimitExceeded) {
		t.Fatalf("err=%v", err)
	}
}

type operatorInvalidPositionFixture struct{ key string }

func (reader operatorInvalidPositionFixture) ListOperations(context.Context, knowl.ScopeRef, OperatorOperationReadOptions) (OperatorReadPage[knowl.OperatorOperationSummary], error) {
	return OperatorReadPage[knowl.OperatorOperationSummary]{NextKey: reader.key}, nil
}

// Catches backend position keys that could page by mutable operation status/time.
func TestOperatorImmutableOperationPositions(t *testing.T) {
	for _, key := range []string{"updated-at-position", `{"updated_at":"2026-10-06T00:00:00Z","operation_id":"operation"}`, `{"created_at":"2026-10-06T00:00:00Z"}`, `{"created_at":"0001-01-01T00:00:00Z","operation_id":"operation"}`, `{"created_at":"2026-10-06T00:00:00Z","operation_id":"operation","status":"failed"}`} {
		service := operatorService(t, OperatorReaders{Operations: operatorInvalidPositionFixture{key: key}})
		_, err := service.Operations(t.Context(), OperatorOperationListOptions{})
		if !errors.Is(err, ErrOperatorWorkspaceUnavailable) {
			t.Fatalf("key=%q err=%v", key, err)
		}
	}
}

// Catches cursor serialization that silently exceeds the public 8KiB bound.
func TestOperatorCursorOutputBound(t *testing.T) {
	reader := &operatorPageFixture{result: OperatorReadPage[knowl.OperatorPageSummary]{SnapshotVersion: operatorSnapshot, NextKey: strings.Repeat("<", 4096)}}
	_, err := operatorService(t, OperatorReaders{Pages: reader}).PageSummaries(t.Context(), OperatorListOptions{})
	if !errors.Is(err, ErrOperatorReadLimitExceeded) {
		t.Fatalf("err=%v", err)
	}
}

// Catches incompatible writer/reader position formats and invalid tuple encoding.
func TestOperatorOperationPositionCodec(t *testing.T) {
	position := OperatorOperationPosition{CreatedAt: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), OperationID: operatorTestOperationID}
	key, err := EncodeOperatorOperationPosition(position)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeOperatorOperationPosition(key)
	if err != nil || decoded.OperationID != operatorTestOperationID || !decoded.CreatedAt.Equal(time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	for _, invalid := range []OperatorOperationPosition{{}, {CreatedAt: position.CreatedAt}, {CreatedAt: position.CreatedAt, OperationID: "op\n"}} {
		_, err := EncodeOperatorOperationPosition(invalid)
		if !errors.Is(err, ErrOperatorInvalidRequest) {
			t.Fatalf("err=%v", err)
		}
	}
}
