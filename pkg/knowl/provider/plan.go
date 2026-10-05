package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"google.golang.org/adk/v2/session"
)

// Plan preserves the base API's single-generation behavior.
func (maintainer *RuntimeMaintainer) Plan(ctx context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
	limits.MaxCorrections = 0
	plan, _, err := maintainer.planSource(ctx, input, limits, nil)
	return plan, legacyCorrectionError(err)
}

// PlanHierarchy preserves single-generation and generic hierarchy validation.
func (maintainer *RuntimeMaintainer) PlanHierarchy(ctx context.Context, input knowl.HierarchyInput) (knowl.HierarchyModelPlan, error) {
	limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
	limits.MaxCorrections = 0
	plan, _, err := maintainer.planHierarchy(ctx, input, limits, nil)
	if err != nil {
		return knowl.HierarchyModelPlan{}, legacyCorrectionError(err)
	}
	normalized, err := app.NormalizeHierarchyInput(input)
	if err != nil {
		return knowl.HierarchyModelPlan{}, err
	}
	if _, err := app.ValidateHierarchyPlan(ctx, normalized, plan, app.HierarchyValidationOptions{}); err != nil {
		return knowl.HierarchyModelPlan{}, fmt.Errorf("validate hierarchy provider plan: %w", err)
	}
	return plan, nil
}

func sourceRequest(ctx context.Context, input knowl.MaintenanceInput, maxInput int) ([]byte, int, error) {
	if input.ContractVersion != app.SourceMaintenanceContractVersion {
		return nil, 0, permanentProviderFailure(reasonProviderInput)
	}
	limits, err := app.NormalizeMaintenanceInputLimits(input.InputLimits)
	if err != nil {
		return nil, 0, permanentProviderFailure(reasonProviderInput)
	}
	input.InputLimits = limits
	envelope, err := app.EncodeSourceMaintenanceRequest(ctx, input)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, correctionContextError(ctx)
		}
		if errors.Is(err, app.ErrMaintenanceInputLimit) {
			return nil, 0, permanentProviderFailure(reasonProviderInputLimit)
		}
		return nil, 0, permanentProviderFailure(reasonProviderInput)
	}
	return envelope, min(limits.MaxRequestBytes, maxInput), nil
}

func hierarchyRequest(input knowl.HierarchyInput) ([]byte, error) {
	normalized, err := app.NormalizeHierarchyInput(input)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return nil, permanentProviderFailure(reasonProviderInput)
	}
	return json.Marshal(struct {
		Operation              string          `json:"operation"`
		Input                  json.RawMessage `json:"input"`
		RequiredSchemaDigest   string          `json:"required_schema_digest"`
		RequiredSnapshotDigest string          `json:"required_snapshot_digest"`
	}{"hierarchy", payload, normalized.SchemaDigest, normalized.SnapshotDigest})
}

func validateOutputBranch(candidate string, required, forbidden []string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(candidate), &fields); err != nil {
		return err
	}
	for _, name := range required {
		if _, exists := fields[name]; !exists {
			return fmt.Errorf("provider output is missing required field %q", name)
		}
	}
	for _, name := range forbidden {
		if _, exists := fields[name]; exists {
			return fmt.Errorf("provider output contains forbidden field %q", name)
		}
	}
	return nil
}

func validatePlanContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("maintainer context is required")
	}
	return ctx.Err()
}

func (maintainer *RuntimeMaintainer) runStructuredPlan(ctx context.Context, envelope []byte, operation string, decode func(string) error) error {
	limits, _ := app.NormalizeOutputSettings(knowl.OutputSettings{})
	limits.MaxCorrections = 0
	limits.MaxOutputBytes = min(limits.MaxOutputBytes, maintainer.maxOutput)
	bounded, cancel := context.WithTimeoutCause(ctx, time.Duration(limits.DeadlineNanos), app.ErrCorrectionDeadline)
	defer cancel()
	_, err := maintainer.runCorrectedPlan(bounded, envelope, operation, limits, decode, nil, "")
	return legacyCorrectionError(err)
}

type maintainerPlanOutput struct {
	CatalogAdditions []knowl.CatalogAddition    `json:"catalog_additions,omitempty"`
	SchemaDigest     string                     `json:"schema_digest"`
	SourceRefs       []string                   `json:"source_refs"`
	Edits            []maintainerFileEditOutput `json:"edits"`
	Rationale        string                     `json:"rationale,omitempty"`
}

type maintainerFileEditOutput struct {
	Path           string `json:"path"`
	ExpectedDigest string `json:"expected_digest,omitempty"`
	Content        string `json:"content"`
}

func (output maintainerPlanOutput) modelPlan() knowl.ModelEditPlan {
	edits := make([]knowl.FileEdit, len(output.Edits))
	for index, edit := range output.Edits {
		edits[index] = knowl.FileEdit{
			Path:           edit.Path,
			ExpectedDigest: edit.ExpectedDigest,
			Content:        []byte(edit.Content),
		}
	}
	return knowl.ModelEditPlan{
		CatalogAdditions: output.CatalogAdditions,
		SchemaDigest:     output.SchemaDigest,
		SourceRefs:       append([]string(nil), output.SourceRefs...),
		Edits:            edits,
		Rationale:        output.Rationale,
	}
}

func planEventText(event *session.Event) string {
	if event == nil || event.Content == nil {
		return ""
	}
	var text strings.Builder
	for _, part := range event.Content.Parts {
		if part != nil && !part.Thought {
			text.WriteString(part.Text)
		}
	}
	return strings.TrimSpace(text.String())
}
