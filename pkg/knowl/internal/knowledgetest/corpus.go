// Package knowledgetest contains the internal deterministic v0.1 knowledge
// loop corpus. It is test infrastructure, not a supported public API.
package knowledgetest

import (
	"crypto/sha256"
	"fmt"
	"time"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	Version              = "v1"
	DecisionPageID       = knowl.PageID("decisions/session-memory")
	DecisionPagePath     = "wiki/decisions/session-memory.md"
	RunbookPageID        = knowl.PageID("runbooks/session-recovery")
	RunbookPagePath      = "wiki/runbooks/session-recovery.md"
	QueryCount           = 12
	MinimumHits          = 11
	EvidenceCharacters   = 1024
	EvidencePages        = 5
	PublicPollDeadline   = 5 * time.Second
	PublicPollInterval   = 10 * time.Millisecond
	inlineAdapter        = "inline"
	decisionOrigin       = "adr-session-memory"
	investigationOrigin  = "investigation-session-recovery"
	runbookOrigin        = "runbook-session-recovery"
	decisionVersionOne   = "1"
	decisionVersionTwo   = "2"
	investigationVersion = "1"
	runbookVersion       = "1"
	markdownMediaType    = "text/markdown"
)

var fixedTime = time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)

// SourceFixture is one immutable public-ingest input.
type SourceFixture struct {
	Origin         string
	Revision       string
	MediaType      string
	Content        string
	ExpectedRef    string
	ExpectedPageID knowl.PageID
}

// QueryFixture is one measured top-five expectation.
type QueryFixture struct {
	Query        string
	ExpectedPage knowl.PageID
	MatchTerms   []string
	ExpectedRef  string
}

var sourceFixtures = []SourceFixture{
	{
		Origin: decisionOrigin, Revision: decisionVersionOne, MediaType: markdownMediaType,
		Content:        "# ADR: Badger for durable session memory\n\nBadger was selected for embedded durable session memory and bounded local recovery.",
		ExpectedRef:    sourceRef(decisionOrigin, decisionVersionOne),
		ExpectedPageID: DecisionPageID,
	},
	{
		Origin: investigationOrigin, Revision: investigationVersion, MediaType: markdownMediaType,
		Content:        "# Investigation: session crash recovery\n\nCrash recovery confirmed durable replay, operation leases, and projection rebuild behavior.",
		ExpectedRef:    sourceRef(investigationOrigin, investigationVersion),
		ExpectedPageID: DecisionPageID,
	},
	{
		Origin: decisionOrigin, Revision: decisionVersionTwo, MediaType: markdownMediaType,
		Content:        "# ADR: SQLite supersedes Badger for active session memory\n\nSQLite is now active, while the historical Badger rationale remains recorded.",
		ExpectedRef:    sourceRef(decisionOrigin, decisionVersionTwo),
		ExpectedPageID: DecisionPageID,
	},
	{
		Origin: runbookOrigin, Revision: runbookVersion, MediaType: markdownMediaType,
		Content:        "# Runbook: session recovery\n\nRestart the sidecar, poll the durable operation, verify its lease, and rebuild projections when required.",
		ExpectedRef:    sourceRef(runbookOrigin, runbookVersion),
		ExpectedPageID: RunbookPageID,
	},
}

var queryFixtures = []QueryFixture{
	{Query: "Why was Badger selected?", ExpectedPage: DecisionPageID, MatchTerms: []string{"badger", "selected"}, ExpectedRef: sourceRef(decisionOrigin, decisionVersionOne)},
	{Query: "durable session memory", ExpectedPage: DecisionPageID, MatchTerms: []string{"durable", "session"}, ExpectedRef: sourceRef(decisionOrigin, decisionVersionOne)},
	{Query: "crash recovery findings", ExpectedPage: DecisionPageID, MatchTerms: []string{"crash", "recovery"}, ExpectedRef: sourceRef(investigationOrigin, investigationVersion)},
	{Query: "What superseded Badger?", ExpectedPage: DecisionPageID, MatchTerms: []string{"superseded", "badger"}, ExpectedRef: sourceRef(decisionOrigin, decisionVersionTwo)},
	{Query: "current SQLite implementation", ExpectedPage: DecisionPageID, MatchTerms: []string{"sqlite", "implementation"}, ExpectedRef: sourceRef(decisionOrigin, decisionVersionTwo)},
	{Query: "historical rationale", ExpectedPage: DecisionPageID, MatchTerms: []string{"historical", "rationale"}, ExpectedRef: sourceRef(decisionOrigin, decisionVersionOne)},
	{Query: "operation lease replay", ExpectedPage: DecisionPageID, MatchTerms: []string{"lease", "replay"}, ExpectedRef: sourceRef(investigationOrigin, investigationVersion)},
	{Query: "projection rebuild evidence", ExpectedPage: DecisionPageID, MatchTerms: []string{"projection", "rebuild"}, ExpectedRef: sourceRef(investigationOrigin, investigationVersion)},
	{Query: "session recovery procedure", ExpectedPage: RunbookPageID, MatchTerms: []string{"recovery", "procedure"}, ExpectedRef: sourceRef(runbookOrigin, runbookVersion)},
	{Query: "restart the sidecar", ExpectedPage: RunbookPageID, MatchTerms: []string{"restart", "sidecar"}, ExpectedRef: sourceRef(runbookOrigin, runbookVersion)},
	{Query: "RUNBOOK, operation polling?", ExpectedPage: RunbookPageID, MatchTerms: []string{"runbook", "operation"}, ExpectedRef: sourceRef(runbookOrigin, runbookVersion)},
	{Query: "How do we verify a recovery lease?", ExpectedPage: RunbookPageID, MatchTerms: []string{"verify", "lease"}, ExpectedRef: sourceRef(runbookOrigin, runbookVersion)},
}

// RestartMatrix names the deterministic v0.1 crash boundaries exercised by
// the repository suites.
var RestartMatrix = []string{
	"accepted before execution",
	"durable stage before status advance",
	"canonical commit before projection",
	"projection before outcome",
	"expired applying lease",
	"cancellation without failure",
	"lost wake-up periodic scan",
}

// Sources returns a deep copy of the ordered immutable source corpus.
func Sources() []SourceFixture {
	return append([]SourceFixture(nil), sourceFixtures...)
}

// Queries returns a deep copy of the measured query corpus.
func Queries() []QueryFixture {
	queries := make([]QueryFixture, len(queryFixtures))
	for index, query := range queryFixtures {
		queries[index] = query
		queries[index].MatchTerms = append([]string(nil), query.MatchTerms...)
	}
	return queries
}

// FinalSnapshot returns the exact final canonical projection fixture.
func FinalSnapshot(scope knowl.ScopeRef) knowl.WorkspaceSnapshot {
	decisionRefs := []string{
		sourceRef(decisionOrigin, decisionVersionOne),
		sourceRef(decisionOrigin, decisionVersionTwo),
		sourceRef(investigationOrigin, investigationVersion),
	}
	runbookRefs := []string{sourceRef(runbookOrigin, runbookVersion)}
	decision := decisionMarkdown(decisionRefs, finalDecision)
	runbook := runbookMarkdown(runbookRefs)
	pages := []knowl.PageSnapshot{
		page(DecisionPageID, DecisionPagePath, "Session Memory Decision", decision, decisionRefs, fixedTime.Add(-time.Hour)),
		page(RunbookPageID, RunbookPagePath, "Session Recovery Runbook", runbook, runbookRefs, fixedTime),
	}
	return knowl.WorkspaceSnapshot{
		Scope: scope, SchemaDigest: "schema-golden-knowledge-loop-v1",
		PageDigests: map[string]string{DecisionPagePath: pages[0].Digest, RunbookPagePath: pages[1].Digest},
		Pages:       pages,
		Links:       []knowl.LinkReference{{From: RunbookPageID, To: DecisionPageID, Relation: "wiki", Untrusted: true}},
		CapturedAt:  fixedTime,
	}
}

func sourceRef(origin, revision string) string {
	return inlineAdapter + ":" + origin + "@" + revision
}

func page(id knowl.PageID, path, title, content string, refs []string, updatedAt time.Time) knowl.PageSnapshot {
	digest := sha256.Sum256([]byte(content))
	return knowl.PageSnapshot{
		ID: id, Path: path, Digest: fmt.Sprintf("%x", digest), Title: title, Content: content,
		SourceRefs: append([]string(nil), refs...), Untrusted: true, UpdatedAt: updatedAt,
	}
}
