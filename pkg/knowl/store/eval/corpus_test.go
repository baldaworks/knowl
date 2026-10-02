package eval_test

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/internal/knowledgetest"
	"github.com/baldaworks/knowl/pkg/knowl/store/internal/lexical"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	evalScope   knowl.ScopeRef = "cpu-model-quality-v1"
	dialogueID  knowl.PageID   = "concepts/dialogue-checkpoints"
	rollbackID  knowl.PageID   = "runbooks/rollback"
	ephemeralID knowl.PageID   = "decoys/ephemeral-dialogue"
	rolloutID   knowl.PageID   = "decoys/rollout"
)

type qualityCase struct {
	ID, Query             string
	Expected              knowl.PageID
	NoOverlap, ExactTitle bool
	Distractor            knowl.PageID
}

// Authored before the first real-model quality run. Expected IDs and k are fixed.
func qualityCases() []qualityCase {
	cases := make([]qualityCase, 0, 16)
	for _, fixture := range knowledgetest.BaselineQueries() {
		cases = append(cases, qualityCase{ID: fixture.ID, Query: fixture.Query, Expected: fixture.Expected[0]})
	}
	return append(cases,
		qualityCase{ID: "english-paraphrase", Query: "Conversations surviving process reboot", Expected: dialogueID, NoOverlap: true, Distractor: ephemeralID},
		qualityCase{ID: "russian-semantic-duplicate", Query: "Отмена неудачного обновления", Expected: rollbackID, NoOverlap: true, Distractor: rolloutID},
		qualityCase{ID: "exact-unique-title", Query: "Dialogue Checkpoints", Expected: dialogueID, ExactTitle: true},
		qualityCase{ID: "mixed-technical-token", Query: "sdkХРАНИЛИЩЕ2", Expected: "concepts/mixed"},
	)
}

func qualitySnapshot() knowl.WorkspaceSnapshot {
	snapshot := knowledgetest.BaselineSnapshot(evalScope)
	rows := [][3]string{
		{string(dialogueID), "Dialogue Checkpoints", "Persist chat history across server restarts using on-disk checkpoints."},
		{string(rollbackID), "Откат релиза", "Возвращаем предыдущую стабильную сборку после ошибки при развёртывании."},
		{"concepts/mixed", "SDKхранилище2", "Mixed technical identifiers remain original evidence."},
		{string(ephemeralID), "Ephemeral dialogue", "Chat history is intentionally discarded at exit; this mode provides no persistence and no recovery."},
		{string(rolloutID), "Развёртывание релиза", "Установка новой сборки в рабочей среде. Процедура не выполняет откат к предыдущей версии."},
		{"decoys/tls", "TLS certificate renewal", "Rotate expired certificates and preserve the private key permissions."},
		{"decoys/routing", "HTTP routing", "Route requests to the correct handler using method and URL paths."},
		{"decoys/compiler", "Compiler warnings", "Promote compiler warnings to errors when building release binaries."},
		{"decoys/heap", "Heap profiling", "Measure allocations and identify retained objects in a heap profile."},
		{"decoys/dns", "DNS service discovery", "Resolve service names using the internal cluster DNS resolver."},
		{"decoys/metrics", "Metrics cardinality", "Avoid user identifiers in metric labels to keep time series bounded."},
		{"decoys/bundles", "JavaScript bundle size", "Remove unused exports to reduce the browser bundle download."},
		{"decoys/cli", "Command completion", "Generate shell completions for typed command line options."},
		{"decoys/clock", "Monotonic clock", "Measure elapsed durations with a monotonic clock rather than wall time."},
		{"decoys/uploads", "Multipart uploads", "Stream file uploads while bounding the size of individual parts."},
		{"decoys/cors", "Cross origin requests", "Allow only approved browser origins in response headers."},
		{"decoys/compression", "Response compression", "Compress large HTTP responses using gzip negotiation."},
		{"decoys/email", "Email delivery", "Validate recipient addresses and configure the SMTP relay."},
		{"decoys/packaging", "Binary packaging", "Package executable files for each operating system and architecture."},
		{"decoys/permissions", "File permissions", "Restrict access to secret files with owner-only filesystem permissions."},
		{"decoys/lint", "Статический анализ", "Проверяем стиль кода и обнаруживаем ошибки до запуска программы."},
		{"decoys/timezone", "Часовые пояса", "Отображаем даты в локальном часовом поясе пользователя."},
		{"decoys/format", "Форматирование текста", "Приводим отступы и переносы строк к единому стилю."},
		{"decoys/pagination", "Pagination cursors", "Use opaque cursors to navigate bounded result sets."},
		{"decoys/images", "Image resizing", "Scale uploaded photographs while preserving their aspect ratio."},
	}
	for _, row := range rows {
		digest := sha256.Sum256([]byte(row[1] + "\n" + row[2]))
		page := knowl.PageSnapshot{ID: knowl.PageID(row[0]), Path: "wiki/" + row[0] + ".md", Title: row[1], Content: row[2], Body: row[2], Digest: fmt.Sprintf("%x", digest), SourceRefs: []string{"fixture:" + row[0] + "@1"}, Untrusted: true, UpdatedAt: snapshot.CapturedAt}
		source := knowl.SourceID("excluded")
		if page.ID == dialogueID {
			source = "eligible"
		}
		page.SourceDocuments = []knowl.SourceDocument{{SourceID: source, DocumentID: knowl.DocumentID(row[0] + ".md"), Revision: "1"}}
		snapshot.Pages = append(snapshot.Pages, page)
		snapshot.PageDigests[page.Path] = page.Digest
	}
	return snapshot
}

func TestQualityCorpusHasIndependentSemanticExpectations(t *testing.T) {
	snapshot := qualitySnapshot()
	decoys := 0
	for _, page := range snapshot.Pages {
		if strings.HasPrefix(string(page.ID), "decoys/") {
			decoys++
		}
	}
	if decoys < 20 || len(knowledgetest.BaselineQueries()) != 9 {
		t.Fatalf("decoys=%d baseline=%d", decoys, len(knowledgetest.BaselineQueries()))
	}
	noOverlap := 0
	for _, fixture := range qualityCases() {
		i := slices.IndexFunc(snapshot.Pages, func(p knowl.PageSnapshot) bool { return p.ID == fixture.Expected })
		if i < 0 {
			t.Fatalf("missing expected page for %s", fixture.ID)
		}
		if !fixture.NoOverlap {
			continue
		}
		query, err := lexical.Normalize(fixture.Query)
		if err != nil {
			t.Fatal(err)
		}
		page := snapshot.Pages[i]
		terms, err := lexical.Normalize(page.Title + "\n" + page.Body)
		if err != nil {
			t.Fatal(err)
		}
		for _, term := range query.Terms {
			if slices.Contains(terms.Terms, term) {
				t.Fatalf("case %s overlaps lexical term %q", fixture.ID, term)
			}
		}
		if len(query.Terms) == 0 || len(terms.Terms) == 0 {
			t.Fatal("empty no-overlap fixture")
		}
		noOverlap++
	}
	if noOverlap < 2 {
		t.Fatalf("no-overlap cases=%d", noOverlap)
	}
}
