package knowledgetest

import (
	"slices"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	BaselineMet         = "met"
	BaselineGap         = "gap"
	BaselineStoragePage = knowl.PageID("decisions/storage")
)

// RecallObservation contains only safe identities and metrics, never source text.
type RecallObservation struct {
	CaseID   string         `json:"case_id"`
	Expected []knowl.PageID `json:"expected"`
	Observed []knowl.PageID `json:"observed"`
	K        int            `json:"k"`
	Hits     int            `json:"hits"`
	Total    int            `json:"total"`
	Recall   float64        `json:"recall"`
	Outcome  string         `json:"outcome"`
}

// ObserveRecall measures unique relevant IDs within k for nonempty curated expectations.
func ObserveRecall(id string, expected, observed []knowl.PageID, k int) RecallObservation {
	result := RecallObservation{CaseID: id, Expected: expected, Observed: observed, K: k, Total: len(expected), Outcome: BaselineGap}
	for _, wanted := range expected {
		if slices.Contains(observed[:min(k, len(observed))], wanted) {
			result.Hits++
		}
	}
	result.Recall = float64(result.Hits) / float64(result.Total)
	if result.Hits == result.Total {
		result.Outcome = BaselineMet
	}
	return result
}

// BaselineQuery is an independently curated retrieval expectation.
type BaselineQuery struct {
	ID       string
	Query    string
	Expected []knowl.PageID
	Control  bool // Existing exact-match successes remain mandatory regression gates.
}

// BaselineQueries uses one path for exact, multilingual and semantic retrieval.
// Epic REQ-RECALL/EMBED-001; Stories .5 and .10.
func BaselineQueries() []BaselineQuery {
	return []BaselineQuery{
		{ID: "exact-body", Query: "quasarretention", Expected: []knowl.PageID{BaselineStoragePage}, Control: true},
		{ID: "word-form-base", Query: "хранилище", Expected: []knowl.PageID{BaselineStoragePage}, Control: true},
		{ID: "word-form-genitive", Query: "хранилища", Expected: []knowl.PageID{BaselineStoragePage}},
		{ID: "word-form-instrumental", Query: "хранилищем", Expected: []knowl.PageID{BaselineStoragePage}},
		{ID: "word-form-english", Query: "archives", Expected: []knowl.PageID{"decisions/archive"}},
		{ID: "mixed-language", Query: "durable хранилище", Expected: []knowl.PageID{BaselineStoragePage}, Control: true},
		{ID: "semantic-duplicate", Query: "resume processing", Expected: []knowl.PageID{"runbooks/recovery"}},
	}
}

// BaselineSource covers source signals; Epic REQ-SOURCE-001, Story .3.
type BaselineSource struct {
	ID           string
	Content      string
	ExpectedPage knowl.PageID
}

// BaselineSources keeps title/body expectations independent of the implementation.
func BaselineSources() []BaselineSource {
	return []BaselineSource{
		{ID: "generic-title", Content: "# Notes\n\nquasarretention records durable storage requirements.", ExpectedPage: BaselineStoragePage},
		{ID: "frontmatter", Content: "---\ntitle: Quasarretention\ntags: [quasarretention]\n---\nA maintenance note.", ExpectedPage: BaselineStoragePage},
		{ID: "fenced-heading", Content: "```markdown\n# Decoy\n```\n\n# Quasarretention\n\nStorage requirements.", ExpectedPage: BaselineStoragePage},
	}
}

// BaselineSnapshot provides the factual evidence behind the curated queries.
func BaselineSnapshot(scope knowl.ScopeRef) knowl.WorkspaceSnapshot {
	pages := []knowl.PageSnapshot{
		page(BaselineStoragePage, "wiki/decisions/storage.md", "Storage decision", "quasarretention: durable хранилище retains project facts.", []string{"fixture:storage@1"}, fixedTime.Add(-48*time.Hour)),
		page("decisions/archive", "wiki/decisions/archive.md", "Retention decision", "archive preserves historical records.", []string{"fixture:archive@1"}, fixedTime.Add(-47*time.Hour)),
		page("runbooks/recovery", "wiki/runbooks/recovery.md", "Recovery procedure", "restart daemon following crash.", []string{"fixture:recovery@1"}, fixedTime.Add(-46*time.Hour)),
	}
	digests := make(map[string]string, len(pages))
	for _, p := range pages {
		digests[p.Path] = p.Digest
	}
	return knowl.WorkspaceSnapshot{Scope: scope, SchemaDigest: "schema-context-baseline-v1", Pages: pages, PageDigests: digests, CapturedAt: fixedTime}
}
