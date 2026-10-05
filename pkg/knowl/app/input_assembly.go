package app

import (
	"context"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/baldaworks/knowl/pkg/knowl/wiki"
)

const sourceEnvelopeFormatVersion = "source-json-v1"

func validRequestFormat(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func sourceRequestBudget(maintainer Maintainer, limits knowl.MaintenanceInputLimits) (knowl.MaintenanceRequestBudget, MaintenanceRequestSizer, error) {
	normalized, err := NormalizeMaintenanceInputLimits(limits)
	if err != nil {
		return knowl.MaintenanceRequestBudget{}, nil, err
	}
	budget := knowl.MaintenanceRequestBudget{MaxBytes: normalized.MaxRequestBytes, FormatVersion: sourceEnvelopeFormatVersion}
	sizer, ok := maintainer.(MaintenanceRequestSizer)
	if ok {
		declared := sizer.RequestBudget()
		if declared.MaxBytes <= 0 || !validRequestFormat(declared.FormatVersion) || declared.ReservedBytes < 0 || declared.ReservedBytes > MaxCorrectionFeedbackBytes {
			return knowl.MaintenanceRequestBudget{}, nil, ErrMaintenanceInputInvalid
		}
		budget.MaxBytes = min(budget.MaxBytes, declared.MaxBytes)
		budget.ReservedBytes = declared.ReservedBytes
		if budget.ReservedBytes >= budget.MaxBytes {
			return knowl.MaintenanceRequestBudget{}, nil, ErrMaintenanceInputInvalid
		}
		budget.FormatVersion = declared.FormatVersion
	}
	return budget, sizer, nil
}

func (service *IngestService) requestBytes(ctx context.Context, input knowl.MaintenanceInput) (int, error) {
	if err := contextErr(ctx); err != nil {
		return 0, err
	}
	var size int
	var err error
	if service.requestSizer != nil {
		size, err = service.requestSizer.RequestBytes(ctx, input)
	} else {
		var encoded []byte
		encoded, err = EncodeSourceMaintenanceRequest(ctx, input)
		size = len(encoded)
	}
	if err != nil {
		return 0, err
	}
	if err := contextErr(ctx); err != nil {
		return 0, err
	}
	if size <= 0 {
		return 0, ErrMaintenanceInputInvalid
	}
	return size, nil
}

type requiredInputLimitError struct{}

func (requiredInputLimitError) Error() string         { return "required maintenance input exceeds budget" }
func (requiredInputLimitError) Unwrap() error         { return ErrMaintenanceInputLimit }
func (requiredInputLimitError) FailureClass() string  { return "input_budget" }
func (requiredInputLimitError) FailureReason() string { return "required_input_limit" }
func (requiredInputLimitError) Retryable() bool       { return false }

// fitSourcePages measures indispensable input first, then reads one whole candidate
// at a time in the index's existing order. It never turns read errors into omissions.
func (service *IngestService) fitSourcePages(ctx context.Context, input knowl.MaintenanceInput, ids []knowl.PageID, entries []knowl.ContextPage) (knowl.MaintenanceInput, sourceFittingEvidence, error) {
	report := sourceFittingEvidence{Budget: knowl.ContextBudget{MaxBytes: service.requestBudget.MaxBytes}, Pages: entries}
	used, err := service.requestBytes(ctx, input)
	if err == nil {
		measured := used
		report.Budget.UsedBytes = &measured
	}
	if errors.Is(err, ErrMaintenanceInputLimit) || (err == nil && used > report.Budget.MaxBytes) {
		return input, report, requiredInputLimitError{}
	}
	if err != nil {
		return input, report, err
	}
	seen := make(map[knowl.PageID]struct{})
	for _, id := range ids {
		if err := contextErr(ctx); err != nil {
			return input, report, err
		}
		if _, ordinary := wiki.PageIDFromPath("wiki/" + string(id) + ".md"); !ordinary {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		if len(seen) >= service.readLimits.Pages {
			break
		}
		seen[id] = struct{}{}
		pages, readErr := service.content.ReadPages(ctx, input.Scope, []knowl.PageID{id}, service.readLimits)
		if readErr != nil {
			return input, report, fmt.Errorf("content: %w", readErr)
		}
		if len(pages) != 1 || pages[0].ID != id {
			return input, report, ErrMaintenanceInputInvalid
		}
		input.Pages = append(input.Pages, pages[0])
		used, err = service.requestBytes(ctx, input)
		if errors.Is(err, ErrMaintenanceInputLimit) || (err == nil && used > report.Budget.MaxBytes) {
			input.Pages[len(input.Pages)-1] = knowl.PageSnapshot{}
			input.Pages = input.Pages[:len(input.Pages)-1]
			report.Budget.OmittedCount++
			report.disposition(id, knowl.ContextBudgetOmitted)
			continue
		}
		if err != nil {
			return input, report, err
		}
		measured := used
		report.Budget.UsedBytes = &measured
		report.Budget.IncludedCount++
		report.disposition(id, knowl.ContextIncluded)
	}
	return input, report, nil
}

type sourceFittingEvidence struct {
	Budget knowl.ContextBudget
	Pages  []knowl.ContextPage
}

func (e *sourceFittingEvidence) disposition(id knowl.PageID, disposition knowl.ContextDisposition) {
	for i := range e.Pages {
		if e.Pages[i].PageID == id {
			e.Pages[i].Disposition = disposition
			return
		}
	}
}

// pendingContext uses exactly the fitter's ordinary/unique allowance, and caps only telemetry.
func pendingContext(ids []knowl.PageID, metadata knowl.ContextSelectionDiagnostics, attempt, limit int) (knowl.OperationContextReport, error) {
	report := knowl.OperationContextReport{Version: 1, WorkAttempt: attempt, Outcome: knowl.ContextAssemblyFailed}
	if metadata.VectorProjection != nil {
		projection := *metadata.VectorProjection
		report.VectorProjection = &projection
	}
	seen := make(map[knowl.PageID]bool)
	for _, id := range ids {
		if _, ordinary := wiki.PageIDFromPath("wiki/" + string(id) + ".md"); !ordinary || seen[id] {
			continue
		}
		if len(seen) >= limit {
			break
		}
		seen[id] = true
		if !validContextPageID(id) || len(report.Pages) >= maxOperationContextPages {
			report.EntriesOmitted++
			continue
		}
		reason := metadata.Reasons[id]
		if reason == "" {
			reason = knowl.ContextUnknown
		}
		report.Pages = append(report.Pages, knowl.ContextPage{PageID: id, SelectionReason: reason, Disposition: knowl.ContextPending})
	}
	count := len(seen)
	report.CandidateCount = &count
	return BoundOperationContextReport(report)
}
