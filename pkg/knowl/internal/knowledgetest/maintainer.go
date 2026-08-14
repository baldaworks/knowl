package knowledgetest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

type decisionStage int

const (
	initialDecision decisionStage = iota
	investigatedDecision
	finalDecision
)

var errUnexpectedCorpusInput = errors.New("unexpected golden corpus input")

// Maintainer deterministically maintains the v1 golden corpus.
type Maintainer struct {
	mu    sync.Mutex
	calls map[string]int
}

// Plan implements app.Maintainer without model or network dependencies.
func (maintainer *Maintainer) Plan(ctx context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	if err := ctx.Err(); err != nil {
		return knowl.ModelEditPlan{}, err
	}
	key := app.SourceRefKey(input.Source)
	maintainer.mu.Lock()
	if maintainer.calls == nil {
		maintainer.calls = make(map[string]int)
	}
	maintainer.calls[key]++
	maintainer.mu.Unlock()

	switch {
	case input.Source.Source.Adapter != inlineAdapter:
		return knowl.ModelEditPlan{}, fmt.Errorf("adapter %q: %w", input.Source.Source.Adapter, errUnexpectedCorpusInput)
	case input.Source.Source.ID == decisionOrigin && input.Source.Version.Version == decisionVersionOne:
		return createDecision(input, key)
	case input.Source.Source.ID == investigationOrigin && input.Source.Version.Version == investigationVersion:
		return updateDecision(input, key, investigatedDecision)
	case input.Source.Source.ID == decisionOrigin && input.Source.Version.Version == decisionVersionTwo:
		return updateDecision(input, key, finalDecision)
	case input.Source.Source.ID == runbookOrigin && input.Source.Version.Version == runbookVersion:
		return createRunbook(input, key)
	default:
		return knowl.ModelEditPlan{}, fmt.Errorf("source %q@%q: %w", input.Source.Source.ID, input.Source.Version.Version, errUnexpectedCorpusInput)
	}
}

// Calls returns the total number of deterministic plans.
func (maintainer *Maintainer) Calls() int {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	total := 0
	for _, count := range maintainer.calls {
		total += count
	}
	return total
}

// CallsFor returns the plan count for one stable source-ref key.
func (maintainer *Maintainer) CallsFor(sourceRef string) int {
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	return maintainer.calls[sourceRef]
}

func createDecision(input knowl.MaintenanceInput, sourceRef string) (knowl.ModelEditPlan, error) {
	if findPage(input.Pages, DecisionPageID) != nil {
		return knowl.ModelEditPlan{}, fmt.Errorf("decision already exists: %w", errUnexpectedCorpusInput)
	}
	refs := []string{sourceRef}
	return plan(input, refs, knowl.FileEdit{Path: DecisionPagePath, Content: []byte(decisionMarkdown(refs, initialDecision))}), nil
}

func updateDecision(input knowl.MaintenanceInput, sourceRef string, stage decisionStage) (knowl.ModelEditPlan, error) {
	if len(input.Pages) == 0 || input.Pages[0].ID != DecisionPageID || input.Pages[0].Path != DecisionPagePath {
		return knowl.ModelEditPlan{}, fmt.Errorf("decision is not first relevant context: %w", errUnexpectedCorpusInput)
	}
	existing := input.Pages[0]
	if strings.TrimSpace(existing.Digest) == "" {
		return knowl.ModelEditPlan{}, fmt.Errorf("decision digest is empty: %w", errUnexpectedCorpusInput)
	}
	refs := mergeRefs(existing.SourceRefs, sourceRef)
	return plan(input, refs, knowl.FileEdit{
		Path: DecisionPagePath, ExpectedDigest: existing.Digest, Content: []byte(decisionMarkdown(refs, stage)),
	}), nil
}

func createRunbook(input knowl.MaintenanceInput, sourceRef string) (knowl.ModelEditPlan, error) {
	if findPage(input.Pages, RunbookPageID) != nil {
		return knowl.ModelEditPlan{}, fmt.Errorf("runbook already exists: %w", errUnexpectedCorpusInput)
	}
	refs := []string{sourceRef}
	return plan(input, refs, knowl.FileEdit{Path: RunbookPagePath, Content: []byte(runbookMarkdown(refs))}), nil
}

func plan(input knowl.MaintenanceInput, refs []string, edit knowl.FileEdit) knowl.ModelEditPlan {
	return knowl.ModelEditPlan{
		SchemaDigest: input.Schema.Digest, SourceRefs: append([]string(nil), refs...),
		Edits: []knowl.FileEdit{edit}, Rationale: "maintain golden project knowledge",
	}
}

func findPage(pages []knowl.PageSnapshot, id knowl.PageID) *knowl.PageSnapshot {
	for index := range pages {
		if pages[index].ID == id {
			return &pages[index]
		}
	}
	return nil
}

func mergeRefs(existing []string, current string) []string {
	refs := append(append([]string(nil), existing...), current)
	sort.Strings(refs)
	unique := refs[:0]
	for _, ref := range refs {
		if len(unique) == 0 || unique[len(unique)-1] != ref {
			unique = append(unique, ref)
		}
	}
	return unique
}

func decisionMarkdown(refs []string, stage decisionStage) string {
	body := "Badger was selected for embedded durable session memory because it offered bounded local persistence.\n"
	if stage >= investigatedDecision {
		body += "\nThe crash recovery investigation confirmed durable replay, operation lease recovery, and projection rebuild evidence.\n"
	}
	if stage >= finalDecision {
		body += "\nCurrent status: SQLite superseded Badger for the active implementation because operational simplicity improved. The historical Badger rationale is retained.\n"
	}
	return frontmatter("decisions/session-memory", "Session Memory Decision", "decision", refs) +
		"# Session Memory Decision\n\n" + body
}

func runbookMarkdown(refs []string) string {
	return frontmatter("runbooks/session-recovery", "Session Recovery Runbook", "runbook", refs) +
		"# Session Recovery Runbook\n\nThis recovery procedure supports [[decisions/session-memory]].\n\n" +
		"1. Restart the sidecar.\n2. Poll the durable operation.\n3. Verify the recovery lease.\n4. Rebuild the projection when required.\n"
}

func frontmatter(id, title, pageType string, refs []string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "---\nid: %s\ntitle: %s\ntype: %s\nsource_refs:\n", id, title, pageType)
	for _, ref := range refs {
		fmt.Fprintf(&builder, "  - %s\n", ref)
	}
	builder.WriteString("---\n")
	return builder.String()
}
