package knowledgetest

import (
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestRecallObservationCountsUniqueHitsWithinK(t *testing.T) {
	t.Parallel()
	const (
		recoveryID = knowl.PageID("recovery")
		storageID  = knowl.PageID("storage")
	)
	expected := []knowl.PageID{storageID, recoveryID}
	observed := []knowl.PageID{storageID, storageID, "unrelated", recoveryID}
	got := ObserveRecall("bounded", expected, observed, 3)
	if got.Hits != 1 || got.Total != 2 || got.Recall != 0.5 || got.Outcome != BaselineGap {
		t.Fatalf("observation = %+v, want one unique hit out of two within k", got)
	}
	complete := ObserveRecall("complete", expected, []knowl.PageID{recoveryID, storageID}, 3)
	if complete.Hits != 2 || complete.Recall != 1 || complete.Outcome != BaselineMet {
		t.Fatalf("improved observation = %+v, want met", complete)
	}
	missing := ObserveRecall("missing", expected, nil, 3)
	if missing.Hits != 0 || missing.Recall != 0 || missing.Outcome != BaselineGap {
		t.Fatalf("empty observation = %+v, want zero recall", missing)
	}
}
