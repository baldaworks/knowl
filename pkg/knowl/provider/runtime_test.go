package provider

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/normahq/runtime/v2/agentfactory"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const (
	hierarchySchemaDigest   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hierarchySnapshotDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testSourcePlanJSON      = `{"schema_digest":"schema","source_refs":[],"edits":[]}`
	testRoadmapPath         = "wiki/concepts/roadmap.md"
	testArchitecturePath    = "wiki/concepts/architecture.md"
	testArchitectureTitle   = "Architecture"
)

func TestMaintainerInstructionRequiresSemanticSynthesisAndProvenance(t *testing.T) {
	for _, required := range []string{
		"entities/",
		"concepts/",
		"syntheses/",
		"Never mirror configured source paths",
		"Merge overlapping evidence",
		"include every source ref used by any edited page",
		"preserve every unrelated existing source ref",
		"preserve every unrelated existing child link",
		"same source/document lineage",
	} {
		if !strings.Contains(maintainerInstruction, required) {
			t.Errorf("maintainer instruction missing %q", required)
		}
	}
}

func TestHierarchyMaintainerInstructionDefinesGenericTaxonomyContract(t *testing.T) {
	for _, required := range []string{
		"subject domains as the primary navigation axis",
		"implementation technology as supporting signals",
		"Recursively split broad heterogeneous groups",
		"Never return an empty catalog",
		"singleton catalog only when",
		"primary subject placement",
		"secondary catalog membership sparingly",
		"Preserve suitable current catalog paths",
		"stable semantic paths and titles",
		"complete final catalog graph",
	} {
		if !strings.Contains(hierarchyMaintainerInstruction, required) {
			t.Errorf("hierarchy maintainer instruction missing %q", required)
		}
	}
	for _, forbidden := range []string{"fastronome", "valera", "billing", "kyc"} {
		if strings.Contains(strings.ToLower(hierarchyMaintainerInstruction), forbidden) {
			t.Errorf("hierarchy maintainer instruction contains tenant-specific term %q", forbidden)
		}
	}
	if !strings.Contains(maintainerInstruction, hierarchyMaintainerInstruction) {
		t.Fatal("composed maintainer instruction omits hierarchy contract")
	}
}

func TestRuntimeMaintainerPlanUsesSelectedRuntime(t *testing.T) {
	plan := maintainerPlanOutput{
		SchemaDigest: "schema-digest",
		SourceRefs:   []string{"source:1"},
		Edits:        []maintainerFileEditOutput{{Path: "wiki/page.md", Content: "# page\n"}},
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

func TestRuntimeMaintainerReusesSessionAcrossPlansAndDeletesOnClose(t *testing.T) {
	planJSON := testSourcePlanJSON
	service := &trackingSessionService{Service: session.InMemoryService()}
	var remoteSessions atomic.Int32
	maintainer, err := newRuntimeMaintainer(
		&fakeRuntimeFactory{agent: newSessionBindingOutputAgent(t, &remoteSessions, planJSON)},
		providerFailureClass,
		t.TempDir(),
		runtimeMaintainerOptions{newSession: func() session.Service { return service }},
	)
	if err != nil {
		t.Fatalf("new maintainer: %v", err)
	}
	if _, err := maintainer.Plan(context.Background(), testMaintenanceInput()); err != nil {
		t.Fatalf("first Plan() error: %v", err)
	}
	if _, err := maintainer.Plan(context.Background(), testMaintenanceInput()); err != nil {
		t.Fatalf("second Plan() error: %v", err)
	}
	if service.creates.Load() != 1 {
		t.Fatalf("created sessions = %d, want one shared maintainer session", service.creates.Load())
	}
	if remoteSessions.Load() != 1 {
		t.Fatalf("remote sessions = %d, want one persisted provider binding", remoteSessions.Load())
	}
	if service.deletes.Load() != 0 {
		t.Fatalf("deleted sessions before Close = %d, want zero", service.deletes.Load())
	}
	if err := maintainer.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if service.deletes.Load() != 1 {
		t.Fatalf("deleted sessions after Close = %d, want one", service.deletes.Load())
	}
}

func TestRuntimeMaintainerPlansHierarchyThroughSharedBoundedSession(t *testing.T) {
	hierarchyInput, hierarchyPlan := testHierarchyInputAndPlan()
	hierarchyJSON, err := json.Marshal(hierarchyPlan)
	if err != nil {
		t.Fatalf("marshal hierarchy plan: %v", err)
	}
	sourceJSON := testSourcePlanJSON
	service := &trackingSessionService{Service: session.InMemoryService()}
	var remoteSessions atomic.Int32
	var prompts []string
	agent := newRoutingSessionBindingOutputAgent(t, &remoteSessions, &prompts, sourceJSON, string(hierarchyJSON))
	factory := &fakeRuntimeFactory{agent: agent}
	maintainer, err := newRuntimeMaintainer(factory, providerFailureClass, t.TempDir(), runtimeMaintainerOptions{
		newSession: func() session.Service { return service },
	})
	if err != nil {
		t.Fatalf("new maintainer: %v", err)
	}
	if _, err := maintainer.Plan(context.Background(), testMaintenanceInput()); err != nil {
		t.Fatalf("source Plan() error: %v", err)
	}
	got, err := maintainer.PlanHierarchy(context.Background(), hierarchyInput)
	if err != nil {
		t.Fatalf("PlanHierarchy() error: %v", err)
	}
	if !reflect.DeepEqual(got, hierarchyPlan) {
		t.Fatalf("PlanHierarchy() = %#v, want %#v", got, hierarchyPlan)
	}
	if factory.builds != 1 || service.creates.Load() != 1 || remoteSessions.Load() != 1 {
		t.Fatalf("shared runtime builds=%d sessions=%d remote=%d", factory.builds, service.creates.Load(), remoteSessions.Load())
	}
	if len(prompts) != 2 {
		t.Fatalf("provider prompts = %d, want two operations", len(prompts))
	}
	hierarchyPrompt := prompts[1]
	for _, want := range []string{
		`"operation":"hierarchy"`,
		`"required_schema_digest":"` + hierarchySchemaDigest + `"`,
		`"required_snapshot_digest":"` + hierarchySnapshotDigest + `"`,
	} {
		if !strings.Contains(hierarchyPrompt, want) {
			t.Fatalf("hierarchy prompt does not contain %q: %s", want, hierarchyPrompt)
		}
	}
	inputMarker := "\nInput JSON:\n"
	inputOffset := strings.LastIndex(hierarchyPrompt, inputMarker)
	if inputOffset < 0 {
		t.Fatalf("hierarchy prompt has no input envelope: %s", hierarchyPrompt)
	}
	hierarchyEnvelope := hierarchyPrompt[inputOffset+len(inputMarker):]
	if strings.Contains(hierarchyEnvelope, "required_source_ref") || strings.Contains(hierarchyEnvelope, "source_refs") ||
		strings.Index(hierarchyEnvelope, testArchitecturePath) > strings.Index(hierarchyEnvelope, testRoadmapPath) {
		t.Fatalf("hierarchy input leaked source context or is not path-sorted: %s", hierarchyEnvelope)
	}
	if err := maintainer.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

func TestRuntimeMaintainerRejectsInvalidHierarchyOutputBeforeReturning(t *testing.T) {
	input, validPlan := testHierarchyInputAndPlan()
	tests := []struct {
		name   string
		output func() string
		want   error
	}{
		{name: "stale snapshot", output: func() string {
			plan := validPlan
			plan.SnapshotDigest = strings.Repeat("f", 64)
			encoded, _ := json.Marshal(plan)
			return string(encoded)
		}, want: app.ErrHierarchyDigestMismatch},
		{name: "source mirror catalog", output: func() string {
			plan := validPlan
			plan.Catalogs = append([]knowl.HierarchyCatalogSpec(nil), plan.Catalogs...)
			plan.Catalogs[1].Path = "wiki/sources/source-a/index.md"
			encoded, _ := json.Marshal(plan)
			return string(encoded)
		}, want: app.ErrHierarchyForbiddenPath},
		{name: "source branch", output: func() string {
			return `{"schema_digest":"` + hierarchySchemaDigest + `","source_refs":[],"edits":[]}`
		}},
		{name: "arbitrary edit", output: func() string {
			return `{"schema_digest":"` + hierarchySchemaDigest + `","snapshot_digest":"` + hierarchySnapshotDigest + `","catalogs":[],"edits":[]}`
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			maintainer, err := newRuntimeMaintainer(
				&fakeRuntimeFactory{agent: newOutputAgent(t, test.output())},
				providerFailureClass,
				t.TempDir(),
				runtimeMaintainerOptions{},
			)
			if err != nil {
				t.Fatalf("new maintainer: %v", err)
			}
			_, err = maintainer.PlanHierarchy(context.Background(), input)
			if err == nil {
				t.Fatal("PlanHierarchy() error = nil")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("PlanHierarchy() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRuntimeMaintainerRejectsOversizedHierarchyInputBeforeBuild(t *testing.T) {
	input, _ := testHierarchyInputAndPlan()
	factory := &fakeRuntimeFactory{agent: newOutputAgent(t, "")}
	maintainer, err := newRuntimeMaintainer(factory, providerFailureClass, t.TempDir(), runtimeMaintainerOptions{maxInputBytes: 64})
	if err != nil {
		t.Fatalf("new maintainer: %v", err)
	}
	_, err = maintainer.PlanHierarchy(context.Background(), input)
	failure, classified := app.ClassifyExecutionFailure(err)
	if err == nil || !classified || failure.Reason != reasonProviderInputLimit || failure.Retryable || factory.builds != 0 {
		t.Fatalf("PlanHierarchy() error=%v builds=%d, want pre-build input limit", err, factory.builds)
	}
}

func TestRuntimeMaintainerRejectsOversizedHierarchyOutput(t *testing.T) {
	input, plan := testHierarchyInputAndPlan()
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal hierarchy plan: %v", err)
	}
	maintainer, err := newRuntimeMaintainer(
		&fakeRuntimeFactory{agent: newOutputAgent(t, string(encoded))},
		providerFailureClass,
		t.TempDir(),
		runtimeMaintainerOptions{maxOutputBytes: 32},
	)
	if err != nil {
		t.Fatalf("new maintainer: %v", err)
	}
	if _, err := maintainer.PlanHierarchy(context.Background(), input); err == nil {
		t.Fatal("PlanHierarchy() error = nil, want bounded provider rejection")
	} else if failure, ok := app.ClassifyExecutionFailure(err); !ok || failure.Reason != reasonProviderOutputLimit || failure.Retryable {
		t.Fatalf("PlanHierarchy() error = %v, classification = %#v/%v", err, failure, ok)
	}
}

func TestRuntimeMaintainerRedactsHierarchyProviderFailure(t *testing.T) {
	input, _ := testHierarchyInputAndPlan()
	secret := "hierarchy-provider-secret"
	maintainer, err := newRuntimeMaintainer(
		&fakeRuntimeFactory{agent: newErrorAgent(t, errors.New(secret))},
		providerFailureClass,
		t.TempDir(),
		runtimeMaintainerOptions{},
	)
	if err != nil {
		t.Fatalf("new maintainer: %v", err)
	}
	_, err = maintainer.PlanHierarchy(context.Background(), input)
	failure, classified := app.ClassifyExecutionFailure(err)
	if err == nil || !classified || failure.Class != providerFailureClass || failure.Reason != reasonProviderRun || !failure.Retryable {
		t.Fatalf("PlanHierarchy() error = %v, classification = %#v/%v", err, failure, classified)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("PlanHierarchy() leaked provider failure: %v", err)
	}
}

func TestRuntimeMaintainerClassifiesAndRedactsBuildFailure(t *testing.T) {
	secret := "provider-build-secret"
	maintainer, err := newRuntimeMaintainer(
		&fakeRuntimeFactory{err: errors.New(secret)},
		providerFailureClass,
		t.TempDir(),
		runtimeMaintainerOptions{},
	)
	if err != nil {
		t.Fatalf("new maintainer: %v", err)
	}
	_, err = maintainer.Plan(context.Background(), testMaintenanceInput())
	failure, classified := app.ClassifyExecutionFailure(err)
	if err == nil || !classified || failure.Class != providerFailureClass || failure.Reason != reasonProviderBuild || !failure.Retryable {
		t.Fatalf("Plan() error = %v, classification = %#v/%v", err, failure, classified)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Plan() leaked provider build failure: %v", err)
	}
}

func newRoutingSessionBindingOutputAgent(t *testing.T, remoteSessions *atomic.Int32, prompts *[]string, sourceOutput, hierarchyOutput string) adkagent.Agent {
	t.Helper()
	agent, err := adkagent.New(adkagent.Config{
		Name:        "routing_session_binding_provider",
		Description: "routes deterministic output while preserving provider session state",
		Run: func(ctx adkagent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {
				if _, stateErr := ctx.Session().State().Get("provider_session"); errors.Is(stateErr, session.ErrStateKeyNotExist) {
					remoteSessions.Add(1)
					if setErr := ctx.Session().State().Set("provider_session", "remote-1"); setErr != nil {
						yield(nil, setErr)
						return
					}
				} else if stateErr != nil {
					yield(nil, stateErr)
					return
				}
				var prompt strings.Builder
				if ctx.UserContent() != nil {
					for _, part := range ctx.UserContent().Parts {
						if part != nil {
							prompt.WriteString(part.Text)
						}
					}
				}
				*prompts = append(*prompts, prompt.String())
				output := sourceOutput
				if strings.Contains(prompt.String(), `"operation":"hierarchy"`) {
					output = hierarchyOutput
				}
				event := session.NewEvent(context.Background(), ctx.InvocationID())
				event.Content = genai.NewContentFromText(output, genai.RoleModel)
				event.TurnComplete = true
				yield(event, nil)
			}
		},
	})
	if err != nil {
		t.Fatalf("create routing output agent: %v", err)
	}
	return agent
}

func newSessionBindingOutputAgent(t *testing.T, remoteSessions *atomic.Int32, output string) adkagent.Agent {
	t.Helper()
	agent, err := adkagent.New(adkagent.Config{
		Name:        "session_binding_provider",
		Description: "simulates a provider binding stored in ADK session state",
		Run: func(ctx adkagent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {
				if _, stateErr := ctx.Session().State().Get("provider_session"); errors.Is(stateErr, session.ErrStateKeyNotExist) {
					remoteSessions.Add(1)
					if setErr := ctx.Session().State().Set("provider_session", map[string]any{"id": "remote-1"}); setErr != nil {
						yield(nil, setErr)
						return
					}
				} else if stateErr != nil {
					yield(nil, stateErr)
					return
				}
				event := session.NewEvent(context.Background(), ctx.InvocationID())
				event.Content = genai.NewContentFromText(output, genai.RoleModel)
				event.TurnComplete = true
				yield(event, nil)
			}
		},
	})
	if err != nil {
		t.Fatalf("create session-binding output agent: %v", err)
	}
	return agent
}

func TestRuntimeMaintainerRejectsUnsafeOutputAndLimits(t *testing.T) {
	secret := "provider-output-secret"
	tests := []struct {
		name       string
		output     string
		maxOutput  int
		wantReason string
	}{
		{name: "malformed", output: secret + " not json", wantReason: reasonProviderOutputInvalid},
		{name: "empty", output: "", wantReason: reasonProviderOutputInvalid},
		{name: "oversized", output: testSourcePlanJSON, maxOutput: 32, wantReason: reasonProviderOutputLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			maintainer, err := newRuntimeMaintainer(
				&fakeRuntimeFactory{agent: newOutputAgent(t, test.output)},
				providerFailureClass,
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
			failure, classified := app.ClassifyExecutionFailure(err)
			if !classified || failure.Class != providerFailureClass || failure.Reason != test.wantReason || failure.Retryable {
				t.Fatalf("Plan() error = %q, classification = %#v/%v", err, failure, classified)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("Plan() leaked provider output: %q", err)
			}
		})
	}
}

func TestRuntimeMaintainerDecodesRepeatedStructuredEvents(t *testing.T) {
	planJSON := `{"schema_digest":"schema","source_refs":["fixture:source@1"],"edits":[]}`
	maintainer, err := newRuntimeMaintainer(
		&fakeRuntimeFactory{agent: newOutputAgent(t, planJSON)},
		providerFailureClass,
		t.TempDir(),
		runtimeMaintainerOptions{newRunner: duplicateOutputRunner},
	)
	if err != nil {
		t.Fatalf("new maintainer: %v", err)
	}
	plan, err := maintainer.Plan(context.Background(), testMaintenanceInput())
	if err != nil {
		t.Fatalf("Plan() repeated structured events: %v", err)
	}
	if plan.SchemaDigest != "schema" {
		t.Fatalf("Plan() = %#v", plan)
	}
}

func duplicateOutputRunner(agent adkagent.Agent, sessions session.Service) (*runner.Runner, error) {
	duplicate, err := adkagent.New(adkagent.Config{
		Name:        "duplicate_output_agent",
		Description: "repeats structured output events",
		Run: func(ctx adkagent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {
				for event, runErr := range agent.Run(ctx) {
					if !yield(event, runErr) || runErr != nil {
						return
					}
					if event != nil && event.Content != nil && !event.TurnComplete {
						if !yield(event, nil) {
							return
						}
					}
				}
			}
		},
	})
	if err != nil {
		return nil, err
	}
	return runner.New(runner.Config{AppName: maintainerAppName, Agent: duplicate, SessionService: sessions})
}

func TestRuntimeMaintainerHonorsCancellationBeforeBuild(t *testing.T) {
	factory := &fakeRuntimeFactory{agent: newOutputAgent(t, testSourcePlanJSON)}
	maintainer, err := newRuntimeMaintainer(factory, providerFailureClass, t.TempDir(), runtimeMaintainerOptions{})
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
	planJSON := testSourcePlanJSON
	started := make(chan struct{})
	factory := &fakeRuntimeFactory{agent: newCancelThenOutputAgent(t, started, planJSON)}
	maintainer, err := newRuntimeMaintainer(factory, providerFailureClass, t.TempDir(), runtimeMaintainerOptions{})
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

func testHierarchyInputAndPlan() (knowl.HierarchyInput, knowl.HierarchyModelPlan) {
	input := knowl.HierarchyInput{
		Scope:           "local",
		SchemaDigest:    hierarchySchemaDigest,
		SnapshotDigest:  hierarchySnapshotDigest,
		MinRootCatalogs: 2,
		Limits:          app.DefaultHierarchyLimits(),
		Pages: []knowl.HierarchyPage{
			{ID: "roadmap", Path: testRoadmapPath, Digest: strings.Repeat("1", 64), Type: "Product", Title: "Roadmap", Tags: []string{"planning", "product"}, Excerpt: "Milestones", Catalogs: []string{fixtureRootCatalogPath}},
			{ID: "architecture", Path: testArchitecturePath, Digest: strings.Repeat("2", 64), Type: testArchitectureTitle, Title: testArchitectureTitle, Tags: []string{"system", "architecture"}, Excerpt: "Components", Catalogs: []string{fixtureRootCatalogPath}},
		},
		Catalogs: []knowl.HierarchyCatalog{{
			Path: fixtureRootCatalogPath, Digest: strings.Repeat("3", 64), Title: "Knowl",
			Children: []string{testRoadmapPath, testArchitecturePath},
		}},
	}
	plan := knowl.HierarchyModelPlan{
		SchemaDigest:   hierarchySchemaDigest,
		SnapshotDigest: hierarchySnapshotDigest,
		Catalogs: []knowl.HierarchyCatalogSpec{
			{Path: fixtureRootCatalogPath, Title: "Knowl", Children: []string{"wiki/catalogs/product/index.md", "wiki/catalogs/architecture/index.md"}},
			{Path: "wiki/catalogs/architecture/index.md", Title: testArchitectureTitle, Children: []string{testArchitecturePath}},
			{Path: "wiki/catalogs/product/index.md", Title: "Product", Children: []string{testRoadmapPath}},
		},
	}
	return input, plan
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

func newErrorAgent(t *testing.T, runErr error) adkagent.Agent {
	t.Helper()
	agent, err := adkagent.New(adkagent.Config{
		Name:        "failing_provider",
		Description: "returns a deterministic provider failure",
		Run: func(adkagent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {
				yield(nil, runErr)
			}
		},
	})
	if err != nil {
		t.Fatalf("create failing output agent: %v", err)
	}
	return agent
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
	err          error
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
	if factory.err != nil {
		return nil, factory.err
	}
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
