package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/baldaworks/knowl/pkg/knowl/wiki"
)

const (
	maxOperationContextBytes = 32 << 10
	maxOperationContextPages = 100
	maxDiagnosticCounter     = 2147483647
)

var ErrOperationContextReportInvalid = errors.New("invalid operation context report")

// DiagnosticContextIndex preserves base ports while reporting actual selection.
type DiagnosticContextIndex interface {
	SelectContextWithDiagnostics(ctx context.Context, scope knowl.ScopeRef, summary knowl.SourceSummary, limits knowl.ReadLimits) ([]knowl.PageID, knowl.RetrievalReport, knowl.ContextSelectionDiagnostics, error)
}

// OperationContextReportStore persists one immutable snapshot for the current attempt.
type OperationContextReportStore interface {
	SaveOperationContextReport(ctx context.Context, scope knowl.ScopeRef, id knowl.OperationID, attempt int, report knowl.OperationContextReport) error
}

// EncodeOperationContextReport validates before publishing or storing evidence.
func EncodeOperationContextReport(report knowl.OperationContextReport) (string, error) {
	if err := validateOperationContext(report); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(report)
	if err != nil || len(encoded) > maxOperationContextBytes {
		return "", ErrOperationContextReportInvalid
	}
	return string(encoded), nil
}

// DecodeOperationContextReport preserves missing and historical evidence and rejects corruption.
func DecodeOperationContextReport(payload string, workAttempt int) (*knowl.OperationContextReport, error) {
	if len(payload) > maxOperationContextBytes || !utf8.ValidString(payload) || !validContextJSONUnicode(payload) {
		return nil, ErrOperationContextReportInvalid
	}
	if strings.TrimSpace(payload) == "" || strings.TrimSpace(payload) == nullReportJSON {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewBufferString(payload))
	decoder.DisallowUnknownFields()
	// Required zero-valued evidence must be explicitly present on the wire.
	var decoded struct {
		knowl.OperationContextReport
		WorkAttempt    *int `json:"work_attempt"`
		EntriesOmitted *int `json:"entries_omitted"`
		Budget         *struct {
			knowl.ContextBudget
			IncludedCount *int `json:"included_count"`
			OmittedCount  *int `json:"omitted_count"`
		} `json:"budget"`
	}
	if err := decoder.Decode(&decoded); err != nil {
		return nil, ErrOperationContextReportInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, ErrOperationContextReportInvalid
	}
	if decoded.WorkAttempt == nil || decoded.EntriesOmitted == nil {
		return nil, ErrOperationContextReportInvalid
	}
	report := decoded.OperationContextReport
	report.WorkAttempt, report.EntriesOmitted = *decoded.WorkAttempt, *decoded.EntriesOmitted
	if decoded.Budget != nil {
		if decoded.Budget.IncludedCount == nil || decoded.Budget.OmittedCount == nil {
			return nil, ErrOperationContextReportInvalid
		}
		budget := decoded.Budget.ContextBudget
		budget.IncludedCount, budget.OmittedCount = *decoded.Budget.IncludedCount, *decoded.Budget.OmittedCount
		report.Budget = &budget
	}
	if report.WorkAttempt > workAttempt {
		return nil, ErrOperationContextReportInvalid
	}
	if _, err := EncodeOperationContextReport(report); err != nil {
		return nil, err
	}
	return &report, nil
}

// BoundOperationContextReport omits diagnostic entries, never model pages or measured counts.
func BoundOperationContextReport(report knowl.OperationContextReport) (knowl.OperationContextReport, error) {
	pages := report.Pages
	report.Pages = nil
	for _, page := range pages {
		if !validContextPageID(page.PageID) || len(report.Pages) >= maxOperationContextPages {
			report.EntriesOmitted++
			continue
		}
		report.Pages = append(report.Pages, page)
	}
	if err := validateOperationContext(report); err != nil {
		return knowl.OperationContextReport{}, err
	}
	for {
		encoded, err := json.Marshal(report)
		if err != nil {
			return knowl.OperationContextReport{}, ErrOperationContextReportInvalid
		}
		if len(encoded) <= maxOperationContextBytes {
			return report, nil
		}
		if len(report.Pages) == 0 {
			return knowl.OperationContextReport{}, ErrOperationContextReportInvalid
		}
		report.Pages = report.Pages[:len(report.Pages)-1]
		report.EntriesOmitted++
	}
}

func validContextPageID(id knowl.PageID) bool {
	if !validDiagnosticIdentifier(string(id), false) {
		return false
	}
	canonical, ordinary := wiki.PageIDFromPath("wiki/" + string(id) + ".md")
	return ordinary && canonical == id
}

func validDiagnosticCount(value int) bool { return value >= 0 && value <= maxDiagnosticCounter }

// JSON otherwise silently replaces unpaired UTF-16 escapes with U+FFFD.
func validContextJSONUnicode(payload string) bool {
	for i := 0; i < len(payload)-1; i++ {
		if payload[i] != '\\' {
			continue
		}
		i++
		if payload[i] != 'u' {
			continue
		}
		if i+4 >= len(payload) {
			return false
		}
		code, err := strconv.ParseUint(payload[i+1:i+5], 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		if code < 0xd800 || code > 0xdbff {
			continue
		}
		if i+6 >= len(payload) || payload[i+1:i+3] != `\u` {
			return false
		}
		low, err := strconv.ParseUint(payload[i+3:i+7], 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}

func validateOperationContext(report knowl.OperationContextReport) error {
	if report.Version != 1 || !validDiagnosticCount(report.WorkAttempt) || !validDiagnosticCount(report.EntriesOmitted) || len(report.Pages) > maxOperationContextPages {
		return ErrOperationContextReportInvalid
	}
	switch report.Outcome {
	case knowl.ContextAssembled, knowl.ContextSelectionFailed, knowl.ContextAssemblyFailed:
	default:
		return ErrOperationContextReportInvalid
	}
	for _, count := range []*int{report.CandidateCount, report.CatalogCount} {
		if count != nil && !validDiagnosticCount(*count) {
			return ErrOperationContextReportInvalid
		}
	}
	if report.CandidateCount == nil {
		if len(report.Pages) != 0 || report.EntriesOmitted != 0 || report.Budget != nil {
			return ErrOperationContextReportInvalid
		}
	} else if *report.CandidateCount != len(report.Pages)+report.EntriesOmitted {
		return ErrOperationContextReportInvalid
	}
	included, omitted, pending := 0, 0, 0
	seen := make(map[knowl.PageID]bool, len(report.Pages))
	for _, page := range report.Pages {
		if !validContextPageID(page.PageID) || seen[page.PageID] {
			return ErrOperationContextReportInvalid
		}
		seen[page.PageID] = true
		switch page.SelectionReason {
		case knowl.ContextLexical, knowl.ContextVector, knowl.ContextHybrid, knowl.ContextNeighbor, knowl.ContextRecent, knowl.ContextUnknown:
		default:
			return ErrOperationContextReportInvalid
		}
		switch page.Disposition {
		case knowl.ContextIncluded:
			included++
		case knowl.ContextBudgetOmitted:
			omitted++
		case knowl.ContextPending:
			pending++
		default:
			return ErrOperationContextReportInvalid
		}
	}
	if budget := report.Budget; budget != nil {
		if budget.MaxBytes <= 0 || budget.MaxBytes > 4<<20 || !validDiagnosticCount(budget.IncludedCount) || !validDiagnosticCount(budget.OmittedCount) || (budget.UsedBytes != nil && !validDiagnosticCount(*budget.UsedBytes)) {
			return ErrOperationContextReportInvalid
		}
		if budget.IncludedCount+budget.OmittedCount > *report.CandidateCount || included > budget.IncludedCount || omitted > budget.OmittedCount || budget.IncludedCount+budget.OmittedCount-included-omitted > report.EntriesOmitted {
			return ErrOperationContextReportInvalid
		}
	}
	if report.Outcome == knowl.ContextAssembled && (report.CandidateCount == nil || report.CatalogCount == nil || report.Budget == nil || report.Budget.UsedBytes == nil || *report.Budget.UsedBytes > report.Budget.MaxBytes || pending != 0 || report.Budget.IncludedCount+report.Budget.OmittedCount != *report.CandidateCount) {
		return ErrOperationContextReportInvalid
	}
	if projection := report.VectorProjection; projection != nil {
		switch projection.State {
		case knowl.VectorNotChecked, knowl.VectorReady:
			if projection.Reason != "" {
				return ErrOperationContextReportInvalid
			}
		case knowl.VectorInvalid:
			if !ValidRetrievalFailure(projection.Reason) {
				return ErrOperationContextReportInvalid
			}
		default:
			return ErrOperationContextReportInvalid
		}
	}
	return nil
}
