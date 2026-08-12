package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// Plan asks the selected runtime provider for one bounded structured edit
// plan. Provider output is validated again by pkg/knowl/app after this method.
func (maintainer *RuntimeMaintainer) Plan(ctx context.Context, input knowl.MaintenanceInput) (knowl.ModelEditPlan, error) {
	if ctx == nil {
		return knowl.ModelEditPlan{}, fmt.Errorf("maintainer context is required")
	}
	if err := ctx.Err(); err != nil {
		return knowl.ModelEditPlan{}, err
	}

	maintainer.mu.Lock()
	defer maintainer.mu.Unlock()
	if maintainer.closed {
		return knowl.ModelEditPlan{}, fmt.Errorf("maintainer is closed")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return knowl.ModelEditPlan{}, fmt.Errorf("encode maintenance input: %w", err)
	}
	if len(payload) > maintainer.maxInput {
		return knowl.ModelEditPlan{}, fmt.Errorf("maintenance input exceeds configured limit")
	}
	envelope, err := json.Marshal(struct {
		Input                json.RawMessage `json:"input"`
		RequiredSchemaDigest string          `json:"required_schema_digest"`
		RequiredSourceRef    string          `json:"required_source_ref"`
	}{
		Input:                payload,
		RequiredSchemaDigest: input.Schema.Digest,
		RequiredSourceRef:    app.SourceRefKey(input.Source),
	})
	if err != nil {
		return knowl.ModelEditPlan{}, fmt.Errorf("encode maintenance request")
	}
	runtime, err := maintainer.ensureRuntime(ctx)
	if err != nil {
		return knowl.ModelEditPlan{}, err
	}

	sessionID := fmt.Sprintf("plan-%d", atomic.AddUint64(&maintainer.sequence, 1))
	_ = runtime.sessions.Delete(ctx, &session.DeleteRequest{
		AppName: maintainerAppName, UserID: maintainerUserID, SessionID: sessionID,
	})
	if _, err := runtime.sessions.Create(ctx, &session.CreateRequest{
		AppName: maintainerAppName, UserID: maintainerUserID, SessionID: sessionID,
	}); err != nil {
		return knowl.ModelEditPlan{}, fmt.Errorf("create maintainer session")
	}
	defer func() {
		_ = runtime.sessions.Delete(context.Background(), &session.DeleteRequest{
			AppName: maintainerAppName, UserID: maintainerUserID, SessionID: sessionID,
		})
	}()

	var (
		plan        knowl.ModelEditPlan
		planFound   bool
		outputBytes int
		decodeErr   error
	)
	for event, runErr := range runtime.runner.Run(
		ctx,
		maintainerUserID,
		sessionID,
		genai.NewContentFromText(string(envelope), genai.RoleUser),
		adkagent.RunConfig{},
	) {
		if runErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return knowl.ModelEditPlan{}, ctxErr
			}
			return knowl.ModelEditPlan{}, fmt.Errorf("run maintainer provider")
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
			return knowl.ModelEditPlan{}, fmt.Errorf("maintainer provider output exceeds configured limit")
		}
		var decoded maintainerPlanOutput
		if err := json.Unmarshal([]byte(candidate), &decoded); err != nil {
			decodeErr = err
			continue
		}
		plan = decoded.modelPlan()
		planFound = true
	}
	if planFound {
		return plan, nil
	}
	if outputBytes == 0 {
		return knowl.ModelEditPlan{}, fmt.Errorf("maintainer provider returned empty output")
	}
	if decodeErr != nil {
		return knowl.ModelEditPlan{}, fmt.Errorf("decode maintainer plan: %w", decodeErr)
	}
	return knowl.ModelEditPlan{}, fmt.Errorf("decode maintainer plan")
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
