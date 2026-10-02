package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/baldaworks/knowl/pkg/knowl/store/sqlite"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	baselineSourceOperation                = "source_maintenance"
	baselineScope           knowl.ScopeRef = "local"
)

func TestContextBaselineProviderInputBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	fixed := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	workspace, err := contentfs.New(t.TempDir(), contentfs.WithClock(func() time.Time { return fixed }))
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	var ids []knowl.PageID
	for i := range 4 {
		id := knowl.PageID(fmt.Sprintf("entities/large-%d", i))
		content := "---\nid: " + string(id) + "\ntitle: Large\ntype: entity\nsource_refs: []\n---\n# Large\n" + strings.Repeat("界<>\n", 32<<10)
		path := filepath.Join(workspace.Root(), "wiki", string(id)+".md")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, fixed, fixed); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// Every page must actually pass the normal filesystem read bounds first.
	pages, err := workspace.ReadPages(ctx, baselineScope, ids, app.DefaultReadLimits())
	if err != nil || len(pages) != len(ids) {
		t.Fatalf("individually bounded pages: count=%d error=%v", len(pages), err)
	}
	input := testMaintenanceInput()
	input.Pages = pages
	input.Schema.Content = []byte("# Schema\n") // Byte slices contribute base64 overhead.
	input.SourceText = "bounded source 界<>"

	t.Run("aggregate-pages", func(t *testing.T) {
		payload, err := app.EncodeSourceMaintenanceRequest(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		var captured string
		factory := &fakeRuntimeFactory{agent: newCapturingOutputAgent(t, &captured, testSourcePlanJSON)}
		maintainer, err := newRuntimeMaintainer(factory, providerFailureClass, t.TempDir(), runtimeMaintainerOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = maintainer.Close() })
		_, err = maintainer.Plan(ctx, input)
		if err != nil {
			t.Fatalf("deduplicated four-page request must fit: %v", err)
		}
		size, sizeErr := maintainer.RequestBytes(ctx, input)
		if sizeErr != nil || size != len(captured) || size > defaultMaxInputBytes || factory.builds != 1 {
			t.Fatalf("measured=%d actual=%d budget=%d err=%v", size, len(captured), defaultMaxInputBytes, sizeErr)
		}
		assertBaselineEnvelope(t, captured, input)
		t.Logf(`{"case_id":"aggregate-input","pages":%d,"input_bytes":%d,"request_bytes":%d,"budget_bytes":%d,"outcome":"met"}`, len(pages), len(payload), len(captured), defaultMaxInputBytes)
	})

	t.Run("serialized-envelope", func(t *testing.T) {
		input.Pages = pages[:1]
		payload, err := app.EncodeSourceMaintenanceRequest(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		var captured string
		factory := &fakeRuntimeFactory{agent: newCapturingOutputAgent(t, &captured, testSourcePlanJSON)}
		maintainer, err := newRuntimeMaintainer(factory, providerFailureClass, t.TempDir(), runtimeMaintainerOptions{maxInputBytes: len(payload)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = maintainer.Close() })
		_, err = maintainer.Plan(ctx, input)
		failure, classified := app.ClassifyExecutionFailure(err)
		if err == nil || !classified || failure.Reason != reasonProviderInputLimit || failure.Retryable || captured != "" || factory.builds != 0 {
			t.Fatalf("envelope-only budget must reject complete wrapper before inference: %v", err)
		}
		t.Logf(`{"case_id":"serialized-envelope","input_bytes":%d,"request_bytes":0,"budget_bytes":%d,"reason":"%s","outcome":"met"}`, len(payload), len(payload), failure.Reason)
	})
}

func assertBaselineEnvelope(t *testing.T, captured string, input knowl.MaintenanceInput) int {
	t.Helper()
	var envelope struct {
		Schema    string                 `json:"required_schema_digest"`
		Operation string                 `json:"operation"`
		Input     knowl.MaintenanceInput `json:"input"`
		Ref       string                 `json:"required_source_ref"`
	}
	// Decode the actual emitted JSON object inside the structured wrapper prompt.
	envelopeBytes := 0
	for line := range strings.Lines(captured) {
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&envelope); err == nil && envelope.Operation == baselineSourceOperation {
			envelopeBytes = len(strings.TrimSpace(line))
			break
		}
	}
	// Compare wire values: omitted empty SourceDocuments is equivalent to nil.
	expected := input
	if expected.InputLimits.MaxRequestBytes == 0 {
		expected.InputLimits.MaxRequestBytes = app.MaxMaintenanceRequestBytes
	}
	expected.Pages = append([]knowl.PageSnapshot{}, input.Pages...)
	for i := range expected.Pages {
		expected.Pages[i].Body = ""
	}
	payload, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	var wireInput knowl.MaintenanceInput
	if err := json.Unmarshal(payload, &wireInput); err != nil {
		t.Fatal(err)
	}
	if envelopeBytes == 0 || envelope.Operation != baselineSourceOperation || envelope.Ref != app.SourceRefKey(input.Source) || envelope.Schema != input.Schema.Digest || !reflect.DeepEqual(envelope.Input, wireInput) {
		t.Fatal("accepted envelope changed authoritative input")
	}
	return envelopeBytes
}

// Only selection order and inference are controlled. Actual content reads,
// application assembly, SDK framing, SQLite operation storage and staging run.
type baselineOrderedIndex struct {
	app.SearchIndex
	ids []knowl.PageID
}

func (i baselineOrderedIndex) SelectContext(context.Context, knowl.ScopeRef, knowl.SourceSummary, knowl.ReadLimits) ([]knowl.PageID, error) {
	return i.ids, nil
}

func TestContextBaselineApplicationInputBudget(t *testing.T) {
	fixed := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	workspace, err := contentfs.New(t.TempDir(), contentfs.WithClock(func() time.Time { return fixed }))
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
	text := "complete source 世界<>"
	digest := sha256.Sum256([]byte(text))
	envelope := knowl.SourceEnvelope{Scope: baselineScope, Source: knowl.SourceRef{Adapter: "fixture", ID: "baseline"}, Version: knowl.SourceVersion{Version: "1", Digest: hex.EncodeToString(digest[:])}, MediaType: "text/plain", Content: []byte(text)}
	accepted, err := workspace.AcceptSource(t.Context(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.ReadFile(filepath.Join(workspace.Root(), "wiki/index.md"))
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]knowl.PageID, 0, 9)
	children := make([]string, 0, 9)
	for i := range 9 {
		id := knowl.PageID(fmt.Sprintf("entities/large-%d", i))
		prose := strings.Repeat("界<>\n", 32<<10)
		if i == 8 {
			id = "entities/later-small"
			prose = "Later small complete evidence.\n"
		}
		content := "---\nid: " + string(id) + "\ntitle: Bounded\ntype: entity\nsource_refs: [" + app.SourceRefKey(accepted) + "]\n---\n# Bounded\n" + prose
		relative := "wiki/" + string(id) + ".md"
		target := filepath.Join(workspace.Root(), relative)
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(target, fixed, fixed); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		children = append(children, relative)
		root = append(root, []byte("\n* [Page]("+string(id)+".md)\n")...)
	}
	if err := os.WriteFile(filepath.Join(workspace.Root(), "wiki/index.md"), root, 0o600); err != nil {
		t.Fatal(err)
	}
	schema, err := workspace.Schema(t.Context(), baselineScope)
	if err != nil {
		t.Fatal(err)
	}
	output, err := json.Marshal(struct {
		SchemaDigest string   `json:"schema_digest"`
		SourceRefs   []string `json:"source_refs"`
		Edits        []any    `json:"edits"`
	}{schema.Digest, []string{app.SourceRefKey(accepted)}, []any{}})
	if err != nil {
		t.Fatal(err)
	}
	var captured string
	factory := &fakeRuntimeFactory{agent: newCapturingOutputAgent(t, &captured, string(output))}
	maintainer, err := newRuntimeMaintainer(factory, providerFailureClass, t.TempDir(), runtimeMaintainerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = maintainer.Close() })
	service, err := app.NewIngestService(workspace, store, baselineOrderedIndex{store, ids}, maintainer, app.IngestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := workspace.Snapshot(t.Context(), baselineScope)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Ingest(t.Context(), envelope)
	if err != nil {
		t.Fatalf("bounded app/runtime inference: %v", err)
	}
	if result.Budget == nil || result.Budget.IncludedCount != 8 || result.Budget.OmittedCount != 1 || result.Budget.MaxBytes != defaultMaxInputBytes || result.Budget.UsedBytes != len(captured) || len(captured) > defaultMaxInputBytes || factory.builds != 1 {
		t.Fatalf("report=%+v actual=%d builds=%d", result.Budget, len(captured), factory.builds)
	}
	expectedIDs := append(append([]knowl.PageID{}, ids[:7]...), ids[8])
	pages, err := workspace.ReadPages(t.Context(), baselineScope, expectedIDs, app.DefaultReadLimits())
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := workspace.Inspect(t.Context(), baselineScope)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(children)
	expected := knowl.MaintenanceInput{ContractVersion: app.SourceMaintenanceContractVersion, CatalogLimits: app.DefaultCatalogLimits(), InputLimits: knowl.MaintenanceInputLimits{MaxRequestBytes: defaultMaxInputBytes}, Scope: baselineScope, Schema: schema, Source: accepted, SourceText: text, Pages: pages, Limits: app.DefaultReadLimits(), Catalogs: []knowl.HierarchyCatalog{{Path: "wiki/index.md", Digest: inspection.Index.Digest, Title: inspection.Index.Title, Children: children}}}
	assertBaselineEnvelope(t, captured, expected)
	after, err := workspace.Snapshot(t.Context(), baselineScope)
	if err != nil || !reflect.DeepEqual(before.PageDigests, after.PageDigests) {
		t.Fatalf("planning changed canonical content: %v", err)
	}
	t.Logf(`{"case_id":"application-input-fitting","candidate_pages":9,"included_pages":8,"omitted_pages":1,"request_bytes":%d,"budget_bytes":%d,"outcome":"met"}`, len(captured), defaultMaxInputBytes)
}
