package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func longOperatorPosition(t *testing.T, revision string) OperatorOperationPosition {
	t.Helper()
	owner := knowl.SourceID(strings.Repeat("s", 64))
	document := knowl.DocumentID(strings.Repeat("d", 1024))
	if err := ValidateSourceDocument(knowl.SourceDocument{SourceID: owner, DocumentID: document, Revision: revision, URI: "file:///a"}); err != nil {
		t.Fatal(err)
	}
	id, err := SourceOperationID(knowl.OperationKey{Scope: operatorTestScope, Source: knowl.SourceRef{Adapter: "wiki-filesystem", ID: string(owner) + "/" + string(document)}, Version: knowl.SourceVersion{Version: revision, Digest: operatorSnapshot}})
	if err != nil {
		t.Fatal(err)
	}
	return OperatorOperationPosition{CreatedAt: time.Unix(100, 0).UTC(), OperationID: id}
}

type operatorPositionReader struct {
	key          string
	continuation OperatorContinuation
}

func (reader *operatorPositionReader) ListOperations(_ context.Context, _ knowl.ScopeRef, options OperatorOperationReadOptions) (OperatorReadPage[knowl.OperatorOperationSummary], error) {
	reader.continuation = options.Continuation
	if options.Continuation.Key != "" {
		return OperatorReadPage[knowl.OperatorOperationSummary]{}, nil
	}
	return OperatorReadPage[knowl.OperatorOperationSummary]{NextKey: reader.key}, nil
}

func TestOperatorLongPositionRoundTrip(t *testing.T) {
	position := longOperatorPosition(t, strings.Repeat("r", 4096))
	encoded, err := EncodeOperatorOperationPosition(position)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeOperatorOperationPosition(encoded)
	if err != nil || decoded != position {
		t.Fatalf("position roundtrip: %+v %v", decoded, err)
	}
	reader := &operatorPositionReader{key: encoded}
	service := operatorService(t, OperatorReaders{Operations: reader})
	first, err := service.Operations(t.Context(), OperatorOperationListOptions{OperatorListOptions: OperatorListOptions{Limit: 1}})
	if err != nil || first.NextCursor == "" || len(first.NextCursor) > 8192 {
		t.Fatalf("long signed cursor: %+v %v", first, err)
	}
	second, err := service.Operations(t.Context(), OperatorOperationListOptions{OperatorListOptions: OperatorListOptions{Limit: 1, Cursor: first.NextCursor}})
	if err != nil || second.NextCursor != "" || reader.continuation.Key != encoded {
		t.Fatalf("signed cursor continuation: %+v %v", reader.continuation, err)
	}
}

func TestOperatorPositionOutputLimits(t *testing.T) {
	for _, test := range []struct {
		name     string
		position OperatorOperationPosition
		want     error
	}{
		{"zero timestamp", OperatorOperationPosition{OperationID: operatorTestOperationID}, ErrOperatorInvalidRequest},
		{"control character", OperatorOperationPosition{CreatedAt: time.Unix(100, 0).UTC(), OperationID: "bad\nidentity"}, ErrOperatorInvalidRequest},
		{"empty identity", OperatorOperationPosition{CreatedAt: time.Unix(100, 0).UTC()}, ErrOperatorInvalidRequest},
		{"oversized identity", OperatorOperationPosition{CreatedAt: time.Unix(100, 0).UTC(), OperationID: knowl.OperationID(strings.Repeat("a", 8193))}, ErrOperatorReadLimitExceeded},
		{"valid provenance JSON expansion", longOperatorPosition(t, strings.Repeat("<", 600)+strings.Repeat("r", 3496)), ErrOperatorReadLimitExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := EncodeOperatorOperationPosition(test.position)
			if !errors.Is(err, test.want) {
				t.Fatalf("encode size/category: %v", err)
			}
		})
	}
	if _, err := DecodeOperatorOperationPosition(strings.Repeat("a", 8193)); !errors.Is(err, ErrOperatorCursorInvalid) {
		t.Fatalf("decode size: %v", err)
	}
	position := longOperatorPosition(t, strings.Repeat("<", 400)+strings.Repeat("r", 3696))
	encoded, err := EncodeOperatorOperationPosition(position)
	if err != nil {
		t.Fatal(err)
	}
	service := operatorService(t, OperatorReaders{Operations: &operatorPositionReader{key: encoded}})
	if _, err := service.Operations(t.Context(), OperatorOperationListOptions{OperatorListOptions: OperatorListOptions{Limit: 1}}); !errors.Is(err, ErrOperatorReadLimitExceeded) {
		t.Fatalf("signed output cap: %v", err)
	}
}
