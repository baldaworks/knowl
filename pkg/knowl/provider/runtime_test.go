package provider

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"sync/atomic"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/normahq/runtime/v2/agentfactory"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const wantMaintainerError = "maintainer"

func TestRuntimeMaintainerPlanUsesSelectedRuntime(t *testing.T) {
	plan := knowl.ModelEditPlan{
		SchemaDigest: "schema-digest",
		SourceRefs:   []string{"source:1"},
		Edits:        []knowl.FileEdit{{Path: "wiki/page.md", Content: []byte("# page\n")}},
		Rationale:    "maintain page",
	}
	encodedPlan, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("encode plan: %v", err)
	}
	var requestPayload string
	agent := newCapturingOutputAgent(t, &requestPayload, string(encodedPlan))
	factory := &fakeRuntimeFactory{agent: agent}
	maintainer, err := newRuntimeMaintainer(factory, "codex", t.TempDir(), runtimeMaintainerOptions{})
	if err != nil {
		t.Fatalf("new maintainer: %v", err)
	}

	got, err := maintainer.Plan(context.Background(), knowl.MaintenanceInput{
		Schema:     knowl.SchemaDocument{Digest: plan.SchemaDigest},
		Source:     knowl.AcceptedSource{Source: knowl.SourceRef{Adapter: "inline", ID: "source-1"}, Version: knowl.SourceVersion{Version: "1"}},
		SourceText: "untrusted source text",
	})
	if err != nil {
		t.Fatalf("Plan() error: %v", err)
	}
	if string(got.Edits[0].Content) != "# page\n" || got.SchemaDigest != plan.SchemaDigest {
		t.Fatalf("Plan() = %#v, want %#v", got, plan)
	}
	if factory.builds != 1 {
		t.Fatalf("provider builds = %d, want one lazy runtime", factory.builds)
	}
	if factory.request.AgentID != "codex" || factory.request.WorkingDirectory == "" {
		t.Fatalf("build request = %#v, want selected provider and workspace", factory.request)
	}
	if factory.request.MCPServerIDs == nil || len(factory.request.MCPServerIDs) != 0 {
		t.Fatalf("MCP server IDs = %#v, want explicit empty list", factory.request.MCPServerIDs)
	}
	if len(factory.request.Tools) != 0 || len(factory.request.Toolsets) != 0 {
		t.Fatalf("build request granted tools: tools=%d toolsets=%d", len(factory.request.Tools), len(factory.request.Toolsets))
	}
	for _, want := range []string{
		`"input":{"scope":""`,
		`"required_schema_digest":"schema-digest"`,
		`"required_source_ref":"inline:source-1@1"`,
	} {
		if !strings.Contains(requestPayload, want) {
			t.Fatalf("maintainer prompt does not contain %q: %s", want, requestPayload)
		}
	}

	if err := maintainer.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if err := maintainer.Close(); err != nil {
		t.Fatalf("second Close() error: %v", err)
	}
	if !factory.closed.Load() {
		t.Fatal("provider agent was not closed")
	}
}

func TestRuntimeMaintainerDeletesEachSession(t *testing.T) {
	planJSON := `{"schema_digest":"schema","source_refs":[],"edits":[]}`
	service := &trackingSessionService{Service: session.InMemoryService()}
	maintainer, err := newRuntimeMaintainer(
		&fakeRuntimeFactory{agent: newOutputAgent(t, planJSON)},
		"provider",
		t.TempDir(),
		runtimeMaintainerOptions{newSession: func() session.Service { return service }},
	)
	if err != nil {
		t.Fatalf("new maintainer: %v", err)
	}
	if _, err := maintainer.Plan(context.Background(), testMaintenanceInput()); err != nil {
		t.Fatalf("Plan() error: %v", err)
	}
	if service.creates.Load() != 1 {
		t.Fatalf("created sessions = %d, want one", service.creates.Load())
	}
	if service.deletes.Load() != 2 {
		t.Fatalf("deleted sessions = %d, want reset plus cleanup", service.deletes.Load())
	}
}

func TestRuntimeMaintainerRejectsUnsafeOutputAndLimits(t *testing.T) {
	secret := "provider-output-secret"
	tests := []struct {
		name      string
		output    string
		maxOutput int
		wantError string
	}{
		{name: "malformed", output: secret + " not json", wantError: wantMaintainerError},
		{name: "empty", output: "", wantError: wantMaintainerError},
		{name: "oversized", output: strings.Repeat("x", 128), maxOutput: 32, wantError: wantMaintainerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			maintainer, err := newRuntimeMaintainer(
				&fakeRuntimeFactory{agent: newOutputAgent(t, test.output)},
				"provider",
				t.TempDir(),
				runtimeMaintainerOptions{maxOutputBytes: test.maxOutput},
			)
			if err != nil {
				t.Fatalf("new maintainer: %v", err)
			}
			_, err = maintainer.Plan(context.Background(), testMaintenanceInput())
			if err == nil {
				t.Fatal("Plan() error = nil, want provider output error")
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Plan() error = %q, want %q", err, test.wantError)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("Plan() leaked provider output: %q", err)
			}
		})
	}
}

func TestRuntimeMaintainerHonorsCancellationBeforeBuild(t *testing.T) {
	factory := &fakeRuntimeFactory{agent: newOutputAgent(t, `{"schema_digest":"schema","source_refs":[],"edits":[]}`)}
	maintainer, err := newRuntimeMaintainer(factory, "provider", t.TempDir(), runtimeMaintainerOptions{})
	if err != nil {
		t.Fatalf("new maintainer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = maintainer.Plan(ctx, testMaintenanceInput())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Plan() error = %v, want context.Canceled", err)
	}
	if factory.builds != 0 {
		t.Fatalf("provider builds = %d, want zero after cancellation", factory.builds)
	}
}

func TestRuntimeMaintainerKeepsCachedRuntimeAfterCanceledPlan(t *testing.T) {
	planJSON := `{"schema_digest":"schema","source_refs":[],"edits":[]}`
	started := make(chan struct{})
	factory := &fakeRuntimeFactory{agent: newCancelThenOutputAgent(t, started, planJSON)}
	maintainer, err := newRuntimeMaintainer(factory, "provider", t.TempDir(), runtimeMaintainerOptions{})
	if err != nil {
		t.Fatalf("new maintainer: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	planErr := make(chan error, 1)
	go func() {
		_, runErr := maintainer.Plan(canceled, testMaintenanceInput())
		planErr <- runErr
	}()
	<-started
	cancel()
	if err := <-planErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled plan error = %v, want context.Canceled", err)
	}
	if factory.closed.Load() {
		t.Fatal("canceled plan closed the cached provider runtime")
	}
	if err := factory.buildContext.Err(); err != nil {
		t.Fatalf("plan cancellation reached host-owned provider context: %v", err)
	}
	if _, err := maintainer.Plan(context.Background(), testMaintenanceInput()); err != nil {
		t.Fatalf("plan after cancellation: %v", err)
	}
	if factory.builds != 1 {
		t.Fatalf("provider builds = %d, want one cached runtime", factory.builds)
	}
	if err := maintainer.Close(); err != nil {
		t.Fatalf("close maintainer: %v", err)
	}
	if !factory.closed.Load() {
		t.Fatal("host-owned maintainer close did not close the provider runtime")
	}
}

func testMaintenanceInput() knowl.MaintenanceInput {
	return knowl.MaintenanceInput{
		Schema: knowl.SchemaDocument{Digest: "schema"},
		Source: knowl.AcceptedSource{
			Source:  knowl.SourceRef{Adapter: "fixture", ID: "source"},
			Version: knowl.SourceVersion{Version: "1"},
		},
	}
}

func newCancelThenOutputAgent(t *testing.T, started chan<- struct{}, output string) adkagent.Agent {
	t.Helper()
	var calls atomic.Int32
	agent, err := adkagent.New(adkagent.Config{
		Name:        "cancel_then_output_provider",
		Description: "deterministic cancellation test provider",
		Run: func(ctx adkagent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {
				if calls.Add(1) == 1 {
					close(started)
					<-ctx.Done()
					yield(nil, ctx.Err())
					return
				}
				event := session.NewEvent(context.Background(), ctx.InvocationID())
				event.Content = genai.NewContentFromText(output, genai.RoleModel)
				if !yield(event, nil) {
					return
				}
				complete := session.NewEvent(context.Background(), ctx.InvocationID())
				complete.TurnComplete = true
				yield(complete, nil)
			}
		},
	})
	if err != nil {
		t.Fatalf("create cancellation test agent: %v", err)
	}
	return agent
}

func newOutputAgent(t *testing.T, output string) adkagent.Agent {
	return newCapturingOutputAgent(t, nil, output)
}

func newCapturingOutputAgent(t *testing.T, prompt *string, output string) adkagent.Agent {
	t.Helper()
	agent, err := adkagent.New(adkagent.Config{
		Name:        "fake_provider",
		Description: "deterministic test provider",
		Run: func(ctx adkagent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {
				if prompt != nil && ctx.UserContent() != nil {
					var request strings.Builder
					for _, part := range ctx.UserContent().Parts {
						if part != nil {
							request.WriteString(part.Text)
						}
					}
					*prompt = request.String()
				}
				if output != "" {
					event := session.NewEvent(context.Background(), ctx.InvocationID())
					event.Content = genai.NewContentFromText(output, genai.RoleModel)
					if !yield(event, nil) {
						return
					}
				}
				complete := session.NewEvent(context.Background(), ctx.InvocationID())
				complete.TurnComplete = true
				yield(complete, nil)
			}
		},
	})
	if err != nil {
		t.Fatalf("create output agent: %v", err)
	}
	return agent
}

type fakeRuntimeFactory struct {
	agent        adkagent.Agent
	request      agentfactory.BuildRequest
	buildContext context.Context
	builds       int
	closed       atomic.Bool
}

func (factory *fakeRuntimeFactory) Build(ctx context.Context, request agentfactory.BuildRequest) (adkagent.Agent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	factory.request = request
	factory.buildContext = ctx
	factory.builds++
	return &closableAgent{Agent: factory.agent, closed: &factory.closed}, nil
}

type closableAgent struct {
	adkagent.Agent
	closed *atomic.Bool
}

func (agent *closableAgent) Close() error {
	agent.closed.Store(true)
	return nil
}

type trackingSessionService struct {
	session.Service
	creates atomic.Int32
	deletes atomic.Int32
}

func (service *trackingSessionService) Create(ctx context.Context, request *session.CreateRequest) (*session.CreateResponse, error) {
	service.creates.Add(1)
	return service.Service.Create(ctx, request)
}

func (service *trackingSessionService) Delete(ctx context.Context, request *session.DeleteRequest) error {
	service.deletes.Add(1)
	return service.Service.Delete(ctx, request)
}
