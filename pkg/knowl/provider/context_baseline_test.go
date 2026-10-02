package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	contentfs "github.com/baldaworks/knowl/pkg/knowl/content/fs"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	baselineGap             = "gap"
	baselineSourceOperation = "source_maintenance"
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
	pages, err := workspace.ReadPages(ctx, "local", ids, app.DefaultReadLimits())
	if err != nil || len(pages) != len(ids) {
		t.Fatalf("individually bounded pages: count=%d error=%v", len(pages), err)
	}
	input := testMaintenanceInput()
	input.Pages = pages
	input.Schema.Content = []byte("# Schema\n") // Byte slices contribute base64 overhead.
	input.SourceText = "bounded source 界<>"

	t.Run("aggregate-pages", func(t *testing.T) {
		payload, err := json.Marshal(input)
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
		outcome, reason := "met", ""
		if err != nil {
			failure, classified := app.ClassifyExecutionFailure(err)
			if !classified || failure.Reason != reasonProviderInputLimit || failure.Retryable || captured != "" || factory.builds != 0 {
				t.Fatalf("aggregate input was not safely rejected before inference: reason=%s classified=%v error=%v", failure.Reason, classified, err)
			}
			outcome, reason = baselineGap, failure.Reason
		} else {
			if len(payload) > maintainer.maxInput {
				t.Fatal("provider accepted input above its configured payload limit")
			}
			assertBaselineEnvelope(t, captured, input)
			if len(captured) > defaultMaxInputBytes {
				outcome = baselineGap
			}
		}
		t.Logf(`{"case_id":"aggregate-input","pages":%d,"input_bytes":%d,"request_bytes":%d,"budget_bytes":%d,"reason":"%s","outcome":"%s"}`, len(pages), len(payload), len(captured), defaultMaxInputBytes, reason, outcome)
	})

	t.Run("serialized-envelope", func(t *testing.T) {
		input.Pages = pages[:1]
		payload, err := json.Marshal(input)
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
		outcome := "met"
		envelopeBytes := 0
		if err != nil {
			failure, classified := app.ClassifyExecutionFailure(err)
			if !classified || failure.Reason != reasonProviderInputLimit || failure.Retryable || captured != "" {
				t.Fatalf("unexpected envelope failure: %v", err)
			}
		} else {
			envelopeBytes = assertBaselineEnvelope(t, captured, input)
			if len(captured) > len(payload) {
				outcome = baselineGap
			}
		}
		t.Logf(`{"case_id":"serialized-envelope","input_bytes":%d,"envelope_bytes":%d,"request_bytes":%d,"budget_bytes":%d,"outcome":"%s"}`, len(payload), envelopeBytes, len(captured), len(payload), outcome)
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
	payload, err := json.Marshal(input)
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
