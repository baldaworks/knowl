package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl"
	"gopkg.in/yaml.v3"
)

const (
	lintError   = "error"
	lintWarning = "warning"
	lintInfo    = "info"
)

// LintOptions configures deterministic lint and optional suggestion-only model lint.
type LintOptions struct {
	ReadLimits knowl.ReadLimits
	Maintainer Maintainer
}

// LintService inspects canonical workspace metadata without mutating it.
type LintService struct {
	content    ContentStore
	index      SearchIndex
	maintainer Maintainer
	limits     knowl.ReadLimits
}

type projectionChecker interface {
	CheckProjection(ctx context.Context, snapshot knowl.WorkspaceSnapshot) error
}

// NewLintService constructs a deterministic lint service.
func NewLintService(content ContentStore, index SearchIndex, options LintOptions) (*LintService, error) {
	if content == nil || index == nil {
		return nil, fmt.Errorf("lint dependencies are required")
	}
	limits, err := normalizeReadLimits(options.ReadLimits)
	if err != nil {
		return nil, err
	}
	return &LintService{content: content, index: index, maintainer: options.Maintainer, limits: limits}, nil
}

// Lint returns deterministic structural, provenance, index, log, and projection findings.
func (service *LintService) Lint(ctx context.Context, scope knowl.ScopeRef) (knowl.LintReport, error) {
	ctx = nonNilContext(ctx)
	if strings.TrimSpace(string(scope)) == "" {
		return knowl.LintReport{}, ErrQueryInvalid
	}
	readCtx, cancel := boundedReadContext(ctx, service.limits)
	defer cancel()
	inspection, err := service.content.Inspect(readCtx, scope)
	if err != nil {
		return knowl.LintReport{}, fmt.Errorf("inspect workspace for lint: %w", err)
	}
	findings := make([]knowl.LintFinding, 0)
	findings = append(findings, lintRawSources(inspection.RawSources)...)
	findings = append(findings, lintPages(inspection.Snapshot, inspection.Index, inspection.RawSources)...)
	findings = append(findings, lintLog(inspection)...)
	if checker, ok := service.index.(projectionChecker); ok {
		if err := checker.CheckProjection(readCtx, inspection.Snapshot); err != nil {
			findings = append(findings, knowl.LintFinding{Code: "projection.drift", Severity: lintError, Message: "SQL/search projection is not ready or does not match canonical Markdown"})
		}
	} else {
		findings = append(findings, knowl.LintFinding{Code: "projection.unavailable", Severity: lintInfo, Message: "configured search index does not expose projection drift checks"})
	}
	if service.maintainer != nil {
		findings = append(findings, service.suggestions(readCtx, inspection, findings)...)
	}
	sortLintFindings(findings)
	return knowl.LintReport{Scope: scope, Findings: findings, CheckedAt: time.Now().UTC()}, nil
}

func lintRawSources(records []knowl.RawSourceRecord) []knowl.LintFinding {
	findings := make([]knowl.LintFinding, 0)
	seen := make(map[string]string)
	for _, record := range records {
		if !record.Valid {
			code := "raw.manifest_invalid"
			if record.ErrorClass != "" {
				code = "raw." + record.ErrorClass
			}
			findings = append(findings, knowl.LintFinding{Code: code, Severity: lintError, Path: record.Path, Message: "raw source manifest or immutable content is invalid"})
			continue
		}
		key := SourceRefKey(record.Source)
		if previous, exists := seen[key]; exists && previous != record.Source.Version.Digest {
			findings = append(findings, knowl.LintFinding{Code: "raw.duplicate_identity", Severity: lintError, Path: record.Path, Message: "one source identity/version has conflicting digests"})
		}
		seen[key] = record.Source.Version.Digest
	}
	return findings
}

func lintPages(snapshot knowl.WorkspaceSnapshot, index knowl.PageSnapshot, records []knowl.RawSourceRecord) []knowl.LintFinding {
	findings := make([]knowl.LintFinding, 0)
	pageIDs := make(map[knowl.PageID]struct{}, len(snapshot.Pages))
	incoming := make(map[knowl.PageID]struct{})
	rawKeys := make(map[string]struct{})
	for _, record := range records {
		if record.Valid {
			rawKeys[SourceRefKey(record.Source)] = struct{}{}
		}
	}
	for _, page := range snapshot.Pages {
		if _, exists := pageIDs[page.ID]; exists {
			findings = append(findings, knowl.LintFinding{Code: "page.duplicate_id", Severity: lintError, Path: page.Path, PageID: page.ID, Message: "multiple canonical pages use the same page ID"})
		}
		pageIDs[page.ID] = struct{}{}
		metadata, err := parseFrontmatter(page.Content)
		if err != nil {
			findings = append(findings, knowl.LintFinding{Code: "frontmatter.malformed", Severity: lintError, Path: page.Path, PageID: page.ID, Message: "page frontmatter is missing or malformed"})
		} else {
			if metadata.ID == "" {
				findings = append(findings, knowl.LintFinding{Code: "frontmatter.id_missing", Severity: lintError, Path: page.Path, PageID: page.ID, Message: "page frontmatter must declare id"})
			} else if metadata.ID != string(page.ID) {
				findings = append(findings, knowl.LintFinding{Code: "frontmatter.id_mismatch", Severity: lintError, Path: page.Path, PageID: page.ID, Message: "frontmatter id does not match the canonical page ID"})
			}
			if metadata.Title == "" {
				findings = append(findings, knowl.LintFinding{Code: "frontmatter.title_missing", Severity: lintError, Path: page.Path, PageID: page.ID, Message: "page frontmatter must declare title"})
			}
			if metadata.Type == "" {
				findings = append(findings, knowl.LintFinding{Code: "frontmatter.type_missing", Severity: lintError, Path: page.Path, PageID: page.ID, Message: "page frontmatter must declare type"})
			}
			if len(metadata.SourceRefs) == 0 {
				findings = append(findings, knowl.LintFinding{Code: "citation.missing", Severity: lintError, Path: page.Path, PageID: page.ID, Message: "page must cite at least one accepted raw source"})
			}
			for _, sourceRef := range metadata.SourceRefs {
				if _, exists := rawKeys[sourceRef]; !exists {
					findings = append(findings, knowl.LintFinding{Code: "citation.unknown_source", Severity: lintError, Path: page.Path, PageID: page.ID, Message: "page cites a raw source that is not present in the workspace", SourceRefs: []string{sourceRef}})
				}
			}
		}
		targets, malformed := markdownTargets(page.Content)
		if malformed {
			findings = append(findings, knowl.LintFinding{Code: "link.malformed", Severity: lintError, Path: page.Path, PageID: page.ID, Message: "page contains an unterminated wiki link"})
		}
		for _, target := range targets {
			if target != "" {
				incoming[knowl.PageID(target)] = struct{}{}
			}
		}
	}
	indexTargets, malformed := indexPageTargets(index.Content)
	if malformed {
		findings = append(findings, knowl.LintFinding{Code: "index.malformed", Severity: lintError, Path: index.Path, Message: "index contains an unterminated wiki link"})
	}
	indexSet := make(map[knowl.PageID]struct{}, len(indexTargets))
	for _, target := range indexTargets {
		pageID := knowl.PageID(target)
		indexSet[pageID] = struct{}{}
		if _, exists := pageIDs[pageID]; !exists {
			findings = append(findings, knowl.LintFinding{Code: "index.broken_page", Severity: lintError, Path: index.Path, PageID: pageID, Message: "index points to a page that does not exist"})
		}
	}
	for _, page := range snapshot.Pages {
		if _, exists := indexSet[page.ID]; !exists {
			findings = append(findings, knowl.LintFinding{Code: "index.missing_page", Severity: lintWarning, Path: index.Path, PageID: page.ID, Message: "canonical page is not listed in index"})
		}
		if _, exists := incoming[page.ID]; !exists {
			if _, indexed := indexSet[page.ID]; !indexed {
				findings = append(findings, knowl.LintFinding{Code: "page.orphan", Severity: lintWarning, Path: page.Path, PageID: page.ID, Message: "page has no incoming wiki link or index entry"})
			}
		}
	}
	for _, link := range snapshot.Links {
		if _, exists := pageIDs[link.To]; !exists {
			findings = append(findings, knowl.LintFinding{Code: "link.broken", Severity: lintError, PageID: link.From, Message: "wiki link points to a missing page", SourceRefs: []string{string(link.To)}})
		}
	}
	return findings
}

func indexPageTargets(content string) ([]string, bool) {
	targets, malformed := markdownTargets(content)
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		if target := normalizePageTarget(strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))); target != "" {
			targets = append(targets, target)
		}
	}
	return targets, malformed
}

type frontmatter struct {
	ID         string   `yaml:"id"`
	Title      string   `yaml:"title"`
	Type       string   `yaml:"type"`
	SourceRefs []string `yaml:"source_refs"`
}

func parseFrontmatter(content string) (frontmatter, error) {
	lines := strings.Split(content, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return frontmatter{}, fmt.Errorf("frontmatter opening delimiter is missing")
	}
	end := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			end = index
			break
		}
	}
	if end < 0 {
		return frontmatter{}, fmt.Errorf("frontmatter closing delimiter is missing")
	}
	var metadata frontmatter
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &metadata); err != nil {
		return frontmatter{}, err
	}
	metadata.ID = strings.TrimSpace(metadata.ID)
	metadata.Title = strings.TrimSpace(metadata.Title)
	metadata.Type = strings.TrimSpace(metadata.Type)
	for index := range metadata.SourceRefs {
		metadata.SourceRefs[index] = strings.TrimSpace(metadata.SourceRefs[index])
	}
	return metadata, nil
}

func markdownTargets(content string) ([]string, bool) {
	targets := make([]string, 0)
	malformed := false
	for offset := 0; offset < len(content); {
		start := strings.Index(content[offset:], "[[")
		if start < 0 {
			break
		}
		start += offset + 2
		end := strings.Index(content[start:], "]]")
		if end < 0 {
			malformed = true
			break
		}
		target := strings.TrimSpace(content[start : start+end])
		if separator := strings.IndexAny(target, "|#"); separator >= 0 {
			target = target[:separator]
		}
		target = normalizePageTarget(target)
		if target != "" {
			targets = append(targets, target)
		}
		offset = start + end + 2
	}
	return targets, malformed
}

func normalizePageTarget(target string) string {
	target = strings.TrimSpace(strings.TrimPrefix(target, "wiki/"))
	target = strings.TrimSuffix(target, ".md")
	if target == "" || target == "." || strings.HasPrefix(target, "/") || strings.HasPrefix(target, "../") || strings.Contains(target, "/../") {
		return ""
	}
	return target
}

type logRecord struct {
	OperationID  string   `json:"operation_id"`
	Generation   string   `json:"generation"`
	SchemaDigest string   `json:"schema_digest"`
	SourceRefs   []string `json:"source_refs"`
	Files        []string `json:"files"`
}

func lintLog(inspection knowl.WorkspaceInspection) []knowl.LintFinding {
	findings := make([]knowl.LintFinding, 0)
	seenOperations := make(map[string]struct{})
	for _, line := range strings.Split(inspection.Log.Content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(trimmed, "- ") {
			findings = append(findings, knowl.LintFinding{Code: "log.malformed", Severity: lintError, Path: inspection.Log.Path, Message: "log contains a non-structured entry"})
			continue
		}
		var record logRecord
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))), &record); err != nil || record.OperationID == "" || record.Generation == "" || len(record.Files) == 0 {
			findings = append(findings, knowl.LintFinding{Code: "log.malformed", Severity: lintError, Path: inspection.Log.Path, Message: "log entry is not a complete structured commit record"})
			continue
		}
		if _, exists := seenOperations[record.OperationID]; exists {
			findings = append(findings, knowl.LintFinding{Code: "log.duplicate_operation", Severity: lintError, Path: inspection.Log.Path, Message: "log contains duplicate operation IDs"})
		}
		seenOperations[record.OperationID] = struct{}{}
		if record.SchemaDigest != inspection.Snapshot.SchemaDigest {
			findings = append(findings, knowl.LintFinding{Code: "log.schema_mismatch", Severity: lintError, Path: inspection.Log.Path, Message: "log entry schema digest differs from the current schema"})
		}
		if !sort.StringsAreSorted(record.Files) {
			findings = append(findings, knowl.LintFinding{Code: "log.order", Severity: lintError, Path: inspection.Log.Path, Message: "log entry file list is not deterministic"})
		}
		for _, path := range record.Files {
			if _, exists := inspection.Snapshot.PageDigests[path]; !exists {
				findings = append(findings, knowl.LintFinding{Code: "log.missing_file", Severity: lintError, Path: path, Message: "log entry cites a file absent from canonical Markdown"})
			}
		}
	}
	return findings
}

func (service *LintService) suggestions(ctx context.Context, inspection knowl.WorkspaceInspection, existing []knowl.LintFinding) []knowl.LintFinding {
	schema, err := service.content.Schema(ctx, inspection.Scope)
	if err != nil {
		return []knowl.LintFinding{{Code: "lint.provider_unavailable", Severity: lintWarning, Message: "optional maintainer lint could not read schema"}}
	}
	summaryBytes, _ := json.Marshal(existing)
	digest := sha256.Sum256(summaryBytes)
	source := knowl.AcceptedSource{Scope: inspection.Scope, Source: knowl.SourceRef{Adapter: "lint", ID: "workspace"}, Version: knowl.SourceVersion{Version: "snapshot", Digest: hex.EncodeToString(digest[:])}}
	pages := append([]knowl.PageSnapshot(nil), inspection.Snapshot.Pages...)
	if len(pages) > service.limits.Pages {
		pages = pages[:service.limits.Pages]
	}
	for index := range pages {
		pages[index].Content = truncateRunes(pages[index].Content, service.limits.Characters)
	}
	plan, err := service.maintainer.Plan(ctx, knowl.MaintenanceInput{Scope: inspection.Scope, Schema: schema, Source: source, SourceText: truncateRunes(string(summaryBytes), service.limits.Characters), Pages: pages, Limits: service.limits})
	if err != nil {
		return []knowl.LintFinding{{Code: "lint.provider_failed", Severity: lintWarning, Message: "optional maintainer lint failed without changing canonical content"}}
	}
	findings := make([]knowl.LintFinding, 0, len(plan.Edits))
	for _, edit := range plan.Edits {
		findings = append(findings, knowl.LintFinding{Code: "lint.suggestion", Severity: lintInfo, Path: edit.Path, Message: "optional maintainer suggested a reviewable edit", Suggestion: true})
	}
	return findings
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func sortLintFindings(findings []knowl.LintFinding) {
	sort.Slice(findings, func(left, right int) bool {
		if findings[left].Code == findings[right].Code {
			if findings[left].Path == findings[right].Path {
				if findings[left].PageID == findings[right].PageID {
					return findings[left].Message < findings[right].Message
				}
				return findings[left].PageID < findings[right].PageID
			}
			return findings[left].Path < findings[right].Path
		}
		return findings[left].Code < findings[right].Code
	})
}
