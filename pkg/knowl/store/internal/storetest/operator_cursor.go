package storetest

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// RunOperatorCursorBounds verifies domain-valid composite IDs through real stores
// and the signing service, including backend JSON and signed-envelope limits.
func RunOperatorCursorBounds(t *testing.T, store app.OperationStore, scope knowl.ScopeRef) {
	t.Helper()
	for _, test := range []struct {
		name, revision string
		limited        bool
	}{
		{"valid_composite", strings.Repeat("r", 4096), false},
		{"signed_envelope_limit", strings.Repeat("<", 400) + strings.Repeat("r", 3696), true},
		{"backend_JSON_limit", strings.Repeat("<", 600) + strings.Repeat("r", 3496), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			scope := childScope(scope, test.name)
			owner := knowl.SourceID(strings.Repeat("s", 64))
			document := knowl.DocumentID(strings.Repeat("d", 1024))
			var ids []knowl.OperationID
			for i := range 2 {
				revision := test.revision[:4095] + string(rune('a'+i))
				key, meta := Fixture(scope, "cursor", time.Unix(100+int64(i), 0).UTC())
				key.Source = knowl.SourceRef{Adapter: "wiki-filesystem", ID: string(owner) + "/" + string(document)}
				key.Version.Version = revision
				meta.Key = key
				meta.AcceptedSource.Source = key.Source
				meta.AcceptedSource.Version = key.Version
				meta.AcceptedSource.SourceDocument = knowl.SourceDocument{SourceID: owner, DocumentID: document, Revision: revision, URI: "file:///a"}
				if err := app.ValidateSourceDocument(meta.AcceptedSource.SourceDocument); err != nil {
					t.Fatal(err)
				}
				r, err := store.Reserve(ctx, key, meta)
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, r.ID)
			}
			service, err := app.NewOperatorService(scope, app.OperatorReaders{Operations: store.(app.OperationLister)}, app.OperatorOptions{})
			if err != nil {
				t.Fatal(err)
			}
			options := app.OperatorOperationListOptions{OperatorListOptions: app.OperatorListOptions{Limit: 1}, SourceID: owner, Status: knowl.StatusReceived}
			first, err := service.Operations(ctx, options)
			if test.limited {
				if !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
					t.Fatalf("materialized cursor budget: %v", err)
				}
				return
			}
			if err != nil || len(first.Items) != 1 || first.Items[0].ID != ids[1] || first.NextCursor == "" || len(first.NextCursor) > 8192 {
				t.Fatalf("valid composite first page: %+v %v", first, err)
			}
			options.Cursor = first.NextCursor
			second, err := service.Operations(ctx, options)
			if err != nil || len(second.Items) != 1 || second.Items[0].ID != ids[0] || second.NextCursor != "" {
				t.Fatalf("valid composite continuation: %+v %v", second, err)
			}
		})
	}
}
