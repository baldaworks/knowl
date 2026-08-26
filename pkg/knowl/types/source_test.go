package knowl_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestSourceDomainJSONRoundTrip(t *testing.T) {
	t.Parallel()
	const (
		testScope        = knowl.ScopeRef("local")
		testSourceID     = knowl.SourceID("engineering")
		testConfigDigest = "config-digest"
		testRunID        = knowl.SyncRunID("run-1")
		testMirrorPath   = "wiki/sources/engineering/architecture/auth.md"
	)
	createdAt := time.Unix(100, 123).UTC()
	updatedAt := time.Unix(200, 456).UTC()
	completedAt := time.Unix(300, 789).UTC()
	ref := knowl.DocumentRef{
		ExternalID: "architecture/auth.md",
		Revision:   "revision-1",
		Path:       "architecture/auth.md",
		Metadata:   map[string]string{"space": string(testSourceID), "owner": "identity"},
	}
	accepted := knowl.AcceptedSource{
		Scope:     testScope,
		Source:    knowl.SourceRef{Adapter: "wiki-filesystem", ID: "engineering/architecture/auth.md"},
		Version:   knowl.SourceVersion{Version: "revision-1", Digest: "source-digest"},
		MediaType: "text/markdown", ManifestRef: "raw/manifest.json",
	}
	sourceDocument := knowl.SourceDocument{
		SourceID: testSourceID, DocumentID: ref.ExternalID, Revision: ref.Revision,
		URI: "https://wiki.example.test/engineering/auth",
	}
	values := []struct {
		name  string
		value any
	}{
		{name: "Source", value: knowl.Source{
			ID: testSourceID, Type: knowl.SourceTypeFilesystem, Enabled: true,
			Config: knowl.SourceConfig{Filesystem: &knowl.FilesystemSourceConfig{
				Root: "/sources/engineering", Include: []string{"**/*.md"}, Flavor: knowl.SourceFlavorObsidian,
				URIBase: "https://wiki.example.test/engineering",
			}},
			Sync:         knowl.SourceSyncPolicy{OnStart: true, Interval: time.Minute, RetryInitial: time.Second, RetryMaximum: time.Minute},
			ConfigDigest: testConfigDigest,
		}},
		{name: "DocumentRef", value: ref},
		{name: "Document", value: knowl.Document{
			DocumentRef: ref, Title: "Authentication", URI: "https://wiki.example.test/engineering/auth",
			MediaType: "text/markdown", Content: []byte("# Authentication\n"),
		}},
		{name: "SourceDocument", value: sourceDocument},
		{name: "SourceMutation", value: knowl.SourceMutation{
			Action: knowl.SourceMutationWrite, Path: testMirrorPath,
			ExpectedDigest: "expected-digest", Content: []byte("# Authentication\n"),
		}},
		{name: "SourceMutationPlan", value: knowl.SourceMutationPlan{
			RunID: testRunID, Scope: testScope, SourceID: testSourceID,
			Mutations: []knowl.SourceMutation{{Action: knowl.SourceMutationDelete, Path: "wiki/sources/engineering/old.md", ExpectedDigest: "expected-digest"}},
		}},
		{name: "StagedSourceMutation", value: knowl.StagedSourceMutation{
			RunID: testRunID, Scope: testScope, SourceID: testSourceID, Generation: "generation-1",
			Files: []string{testMirrorPath}, CreatedAt: createdAt,
		}},
		{name: "SyncRun", value: knowl.SyncRun{
			ID: testRunID, Scope: testScope, SourceID: testSourceID, ConfigDigest: testConfigDigest,
			Status: knowl.SyncStatusSucceeded, Cursor: "cursor-1", NextPageToken: "page-2", CompleteScan: true,
			Counts:       knowl.SyncCounts{Added: 1, Updated: 2, Unchanged: 3, Deleted: 4, Failed: 5},
			FailureClass: "none", ContentGeneration: "generation-1", Checkpoint: "checkpoint-1",
			StartedAt: createdAt, UpdatedAt: updatedAt, CompletedAt: completedAt,
		}},
		{name: "DocumentState", value: knowl.DocumentState{
			Scope: testScope, SourceID: testSourceID, DocumentID: ref.ExternalID, Revision: ref.Revision,
			AcceptedSource: accepted, MirrorPath: testMirrorPath,
			MirrorDigest: "mirror-digest", LastSeenRunID: testRunID, Deleted: true, DeletedAt: completedAt,
			CreatedAt: createdAt, UpdatedAt: updatedAt,
		}},
		{name: "SourceStatus", value: knowl.SourceStatus{
			Scope: testScope, SourceID: testSourceID, Type: knowl.SourceTypeFilesystem, ConfigDigest: testConfigDigest,
			Checkpoint: "checkpoint-1", LastAttemptRunID: "run-2", LastSuccessfulRunID: testRunID,
			Status: knowl.SyncStatusFailed, Counts: knowl.SyncCounts{Failed: 1}, CreatedAt: createdAt,
			Maintenance: knowl.SourceMaintenanceStatus{
				Counts:    knowl.MaintenanceCounts{Queued: 1, Replayed: 2, Committed: 3, Failed: 4},
				Samples:   []knowl.MaintenanceSample{{DocumentID: ref.ExternalID, Revision: ref.Revision, OperationID: "operation-1", Status: knowl.StatusFailed, Replayed: true, FailureClass: "provider"}},
				Truncated: true,
			},
			LastAttemptAt: completedAt, LastSuccessfulAt: updatedAt, UpdatedAt: completedAt,
		}},
	}
	for _, test := range values {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			decoded := reflect.New(reflect.TypeOf(test.value))
			if err := json.Unmarshal(encoded, decoded.Interface()); err != nil {
				t.Fatal(err)
			}
			if got := decoded.Elem().Interface(); !reflect.DeepEqual(got, test.value) {
				t.Fatalf("round trip = %#v, want %#v", got, test.value)
			}
		})
	}
}
