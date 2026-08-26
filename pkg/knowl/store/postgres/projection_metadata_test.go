package postgres

import (
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/store/internal/searchtest"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestSnapshotDigestIncludesOKFProjectionValues(t *testing.T) {
	snapshot := searchtest.Snapshot()
	original := snapshotDigest(snapshot)
	changed := snapshot
	changed.Pages = append([]knowl.PageSnapshot(nil), snapshot.Pages...)
	for index := range changed.Pages {
		if changed.Pages[index].OKF == nil {
			continue
		}
		metadata := *changed.Pages[index].OKF
		metadata.Description += " changed"
		changed.Pages[index].OKF = &metadata
		break
	}
	if original == "" || snapshotDigest(changed) == original {
		t.Fatal("snapshot digest ignored OKF metadata")
	}
}

func TestSnapshotDigestIncludesResolvedSourceDocuments(t *testing.T) {
	snapshot := searchtest.Snapshot()
	original := snapshotDigest(snapshot)
	changed := snapshot
	changed.Pages = append([]knowl.PageSnapshot(nil), snapshot.Pages...)
	for index := range changed.Pages {
		if len(changed.Pages[index].SourceDocuments) == 0 {
			continue
		}
		changed.Pages[index].SourceDocuments = append([]knowl.SourceDocument(nil), changed.Pages[index].SourceDocuments...)
		changed.Pages[index].SourceDocuments[0].Revision += "-changed"
		break
	}
	if original == "" || snapshotDigest(changed) == original {
		t.Fatal("snapshot digest ignored resolved source documents")
	}
}
