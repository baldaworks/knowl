package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const correctionMixedExhaustion = "mixed exhaustion"

// Readiness is explicit: this fixture evaluates planning, not wall-clock scheduling.
type correctionApplicationOperations struct{ *sqlite.Store }

func (s correctionApplicationOperations) Reserve(ctx context.Context, key knowl.OperationKey, meta knowl.OperationMeta) (app.OperationReservation, error) {
	meta.CreatedAt = time.Unix(1, 0).UTC()
	return s.Store.Reserve(ctx, key, meta)
}

func TestRuntimeCorrectionThroughApplication(t *testing.T) {
	for _, mode := range []string{"schema rejection", "provenance", correctionMixedExhaustion} {
		t.Run(mode, func(t *testing.T) {
			workspace, err := contentfs.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := workspace.Init(); err != nil {
				t.Fatal(err)
			}
			store, err := sqlite.Open(t.Context(), filepath.Join(workspace.Root(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			root, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki/index.md"))
			if err != nil {
				t.Fatal(err)
			}
			var id knowl.OperationID
			var prompts []string
			outputBytes := 0
			agent, err := adkagent.New(adkagent.Config{Name: "app-correction", Run: func(ctx adkagent.InvocationContext) iter.Seq2[*session.Event, error] {
				return func(yield func(*session.Event, error) bool) {
					if len(prompts) >= 2 {
						t.Error("correction exceeded the single allowance")
						return
					}
					var prompt strings.Builder
					for _, part := range ctx.UserContent().Parts {
						prompt.WriteString(part.Text)
					}
					prompts = append(prompts, prompt.String())
					envelope := correctionEnvelope(t, prompt.String())
					var input knowl.MaintenanceInput
					if err := json.Unmarshal(envelope["input"], &input); err != nil {
						t.Fatal(err)
					}
					if _, err := workspace.LoadStage(t.Context(), "local", id); !errors.Is(err, app.ErrStageNotFound) {
						t.Fatalf("candidate staged before final validation: %v", err)
					}
					op, err := store.Operation(t.Context(), "local", id)
					if err != nil || op.Plan != nil || op.Correction != nil {
						t.Fatalf("intermediate candidate persisted: %+v %v", op, err)
					}
					current, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki/index.md"))
					if err != nil || string(current) != string(root) {
						t.Fatal("intermediate candidate changed canonical content")
					}
					plan := maintainerPlanOutput{SchemaDigest: input.Schema.Digest, SourceRefs: []string{app.SourceRefKey(input.Source)}, Edits: []maintainerFileEditOutput{}}
					if len(prompts) == 1 || mode == correctionMixedExhaustion {
						if mode == "schema rejection" {
							plan.SchemaDigest = "incorrect"
						} else {
							plan.SourceRefs = []string{"fixture:unauthorized@1"}
						}
					}
					encoded, err := json.Marshal(plan)
					if err != nil {
						t.Fatal(err)
					}
					if len(prompts) == 1 && mode == correctionMixedExhaustion {
						encoded = []byte("malformed")
					}
					outputBytes += len(encoded)
					event := session.NewEvent(context.Background(), ctx.InvocationID())
					event.Content = genai.NewContentFromText(string(encoded), genai.RoleModel)
					event.TurnComplete = true
					yield(event, nil)
				}
			}})
			if err != nil {
				t.Fatal(err)
			}
			m, err := newRuntimeMaintainer(&fakeRuntimeFactory{agent: agent}, providerFailureClass, workspace.Root(), runtimeMaintainerOptions{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close() })
			service, err := app.NewIngestService(workspace, correctionApplicationOperations{store}, store, m, app.IngestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			content := []byte("complete authoritative evidence")
			digest := sha256.Sum256(content)
			submission, err := service.Submit(t.Context(), knowl.SourceEnvelope{Scope: "local", Source: knowl.SourceRef{Adapter: "application-fixture", ID: "application"}, Version: knowl.SourceVersion{Version: "1", Digest: hex.EncodeToString(digest[:])}, MediaType: "text/plain", Content: content})
			if err != nil {
				t.Fatal(err)
			}
			id = submission.Operation.ID
			result, err := service.Execute(t.Context(), submission)
			outcome := knowl.CorrectionAccepted
			if mode == correctionMixedExhaustion {
				outcome = knowl.CorrectionExhausted
				if !errors.Is(err, app.ErrOutputCorrectionExhausted) || result.Operation.Plan != nil {
					t.Fatalf("mixed-layer bound: %+v %v", result, err)
				}
			} else if err != nil || result.Operation.Status != knowl.StatusAwaitingReview {
				t.Fatalf("corrected application plan: %+v %v", result, err)
			}
			op, readErr := store.Operation(t.Context(), "local", id)
			if readErr != nil || op.Correction == nil || op.Correction.Outcome != outcome || *op.Correction.Turns != 2 || *op.Correction.Corrections != 1 || *op.Correction.OutputBytes != outputBytes || op.WorkAttempt != 1 || op.RetryAttempt != 1 {
				t.Fatalf("physical evidence differs from app counters: %+v %v", op, readErr)
			}
			initial, corrected := correctionEnvelope(t, prompts[0]), correctionEnvelope(t, prompts[1])
			delete(corrected, "validation_feedback")
			if !reflect.DeepEqual(initial, corrected) {
				t.Fatal("correction changed captured input")
			}
		})
	}
}
