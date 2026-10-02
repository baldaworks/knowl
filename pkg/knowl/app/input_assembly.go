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
		if declared.MaxBytes <= 0 || !validRequestFormat(declared.FormatVersion) {
			return knowl.MaintenanceRequestBudget{}, nil, ErrMaintenanceInputInvalid
		}
		budget.MaxBytes = min(budget.MaxBytes, declared.MaxBytes)
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
func (service *IngestService) fitSourcePages(ctx context.Context, input knowl.MaintenanceInput, ids []knowl.PageID) (knowl.MaintenanceInput, knowl.MaintenanceBudgetReport, error) {
	report := knowl.MaintenanceBudgetReport{MaxBytes: service.requestBudget.MaxBytes}
	used, err := service.requestBytes(ctx, input)
	if errors.Is(err, ErrMaintenanceInputLimit) || (err == nil && used > report.MaxBytes) {
		return input, report, requiredInputLimitError{}
	}
	if err != nil {
		return input, report, err
	}
	report.UsedBytes = used
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
		if errors.Is(err, ErrMaintenanceInputLimit) || (err == nil && used > report.MaxBytes) {
			input.Pages[len(input.Pages)-1] = knowl.PageSnapshot{}
			input.Pages = input.Pages[:len(input.Pages)-1]
			report.OmittedCount++
			continue
		}
		if err != nil {
			return input, report, err
		}
		report.UsedBytes = used
		report.IncludedCount++
	}
	return input, report, nil
}
