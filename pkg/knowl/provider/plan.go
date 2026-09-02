package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/normahq/runtime/v2/structuredagent"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// Plan asks the selected runtime provider for one bounded structured edit
// plan. Provider output is validated again by pkg/knowl/app after this method.
func (maintainer *RuntimeMaintainer) Plan(ctx context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	if err := validatePlanContext(ctx); err != nil {
		return knowl.ModelEditPlan{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return knowl.ModelEditPlan{}, permanentProviderFailure(reasonProviderInput)
	}
	if len(payload) > maintainer.maxInput {
		return knowl.ModelEditPlan{}, permanentProviderFailure(reasonProviderInputLimit)
	}
	envelope, err := json.Marshal(struct {
		Operation            string          `json:"operation"`
		Input                json.RawMessage `json:"input"`
		RequiredSchemaDigest string          `json:"required_schema_digest"`
		RequiredSourceRef    string          `json:"required_source_ref"`
	}{
		Operation:            "source_maintenance",
		Input:                payload,
		RequiredSchemaDigest: input.Schema.Digest,
		RequiredSourceRef:    app.SourceRefKey(input.Source),
	})
	if err != nil {
		return knowl.ModelEditPlan{}, permanentProviderFailure(reasonProviderInput)
	}
	var plan knowl.ModelEditPlan
	err = maintainer.runStructuredPlan(ctx, envelope, "maintainer", func(candidate string) error {
		if branchErr := validateOutputBranch(candidate, []string{"source_refs", "edits"}, []string{"snapshot_digest", "catalogs"}); branchErr != nil {
			return branchErr
		}
		var decoded maintainerPlanOutput
		if decodeErr := json.Unmarshal([]byte(candidate), &decoded); decodeErr != nil {
			return decodeErr
		}
		plan = decoded.modelPlan()
		return nil
	})
	if err != nil {
		return knowl.ModelEditPlan{}, err
	}
	return plan, nil
}

// PlanHierarchy asks the selected runtime provider for a catalog graph only.
// The same lazy runtime and ADK session are shared with source maintenance.
func (maintainer *RuntimeMaintainer) PlanHierarchy(ctx context.Context, input knowl.HierarchyInput) (knowl.HierarchyModelPlan, error) {
	if err := validatePlanContext(ctx); err != nil {
		return knowl.HierarchyModelPlan{}, err
	}
	normalized, err := app.NormalizeHierarchyInput(input)
	if err != nil {
		return knowl.HierarchyModelPlan{}, err
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return knowl.HierarchyModelPlan{}, permanentProviderFailure(reasonProviderInput)
	}
	if len(payload) > maintainer.maxInput {
		return knowl.HierarchyModelPlan{}, permanentProviderFailure(reasonProviderInputLimit)
	}
	envelope, err := json.Marshal(struct {
		Operation              string          `json:"operation"`
		Input                  json.RawMessage `json:"input"`
		RequiredSchemaDigest   string          `json:"required_schema_digest"`
		RequiredSnapshotDigest string          `json:"required_snapshot_digest"`
	}{
		Operation:              "hierarchy",
		Input:                  payload,
		RequiredSchemaDigest:   normalized.SchemaDigest,
		RequiredSnapshotDigest: normalized.SnapshotDigest,
	})
	if err != nil {
		return knowl.HierarchyModelPlan{}, permanentProviderFailure(reasonProviderInput)
	}
	var plan knowl.HierarchyModelPlan
	err = maintainer.runStructuredPlan(ctx, envelope, "hierarchy", func(candidate string) error {
		if branchErr := validateOutputBranch(candidate, []string{"snapshot_digest", "catalogs"}, []string{"source_refs", "edits", "rationale"}); branchErr != nil {
			return branchErr
		}
		return json.Unmarshal([]byte(candidate), &plan)
	})
	if err != nil {
		return knowl.HierarchyModelPlan{}, err
	}
	if _, err := app.ValidateHierarchyPlan(ctx, normalized, plan, app.HierarchyValidationOptions{}); err != nil {
		return knowl.HierarchyModelPlan{}, fmt.Errorf("validate hierarchy provider plan: %w", err)
	}
	return plan, nil
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
	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	if maintainer.closed {
		return fmt.Errorf("maintainer is closed")
	}
	runtime, err := maintainer.ensureRuntime(ctx)
	if err != nil {
		return err
	}
	var (
		planFound   bool
		outputBytes int
		decodeErr   error
	)
	for event, runErr := range runtime.runner.Run(
		ctx,
		maintainerUserID,
		runtime.sessionID,
		genai.NewContentFromText(string(envelope), genai.RoleUser),
		adkagent.RunConfig{},
	) {
		if runErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if errors.Is(runErr, structuredagent.ErrStructuredInputSchemaValidation) {
				return permanentProviderFailure(reasonProviderInput)
			}
			if errors.Is(runErr, structuredagent.ErrStructuredOutputSchemaValidation) {
				return permanentProviderFailure(reasonProviderOutputInvalid)
			}
			return transientProviderFailure(reasonProviderRun)
		}
		if event == nil || event.Content == nil {
			continue
		}
		candidate := planEventText(event)
		if candidate == "" {
			continue
		}
		outputBytes += len(candidate)
		if outputBytes > maintainer.maxOutput {
			return permanentProviderFailure(reasonProviderOutputLimit)
		}
		if err := decode(candidate); err != nil {
			decodeErr = err
			continue
		}
		planFound = true
	}
	if planFound {
		return nil
	}
	if outputBytes == 0 {
		return permanentProviderFailure(reasonProviderOutputEmpty)
	}
	if decodeErr != nil {
		return permanentProviderFailure(reasonProviderOutputInvalid)
	}
	return permanentProviderFailure(reasonProviderOutputInvalid)
}

type maintainerPlanOutput struct {
	SchemaDigest string                     `json:"schema_digest"`
	SourceRefs   []string                   `json:"source_refs"`
	Edits        []maintainerFileEditOutput `json:"edits"`
	Rationale    string                     `json:"rationale,omitempty"`
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
		SchemaDigest: output.SchemaDigest,
		SourceRefs:   append([]string(nil), output.SourceRefs...),
		Edits:        edits,
		Rationale:    output.Rationale,
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
