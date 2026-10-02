package app

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	requestMetadataType = "topic"
	requestExtensionKey = "custom"
)

func TestMaintenanceInputLimitNormalization(t *testing.T) {
	for _, n := range []int{0, 1, MaxMaintenanceRequestBytes, -1, MaxMaintenanceRequestBytes + 1} {
		got, err := NormalizeMaintenanceInputLimits(knowl.MaintenanceInputLimits{MaxRequestBytes: n})
		if n < 0 || n > MaxMaintenanceRequestBytes {
			if !errors.Is(err, ErrMaintenanceInputInvalid) {
				t.Fatalf("limit %d error=%v", n, err)
			}
			continue
		}
		want := n
		if want == 0 {
			want = MaxMaintenanceRequestBytes
		}
		if err != nil || got.MaxRequestBytes != want {
			t.Fatalf("limit=%d got=%v err=%v", n, got, err)
		}
	}
}
func TestSourceRequestWirePreservesCompleteContentOnce(t *testing.T) {
	input := requestTestInput()
	input.Source.MediaType = "text/plain"
	input.Source.ManifestRef = "raw/fixture/source/1/manifest.json"
	input.Source.SourceDocument = knowl.SourceDocument{SourceID: "engineering", DocumentID: "notes/one", Revision: "rev1", URI: "file:///notes/one"}
	input.Pages[0].SourceDocuments = []knowl.SourceDocument{input.Source.SourceDocument}
	input.Catalogs = []knowl.HierarchyCatalog{{Path: "wiki/index.md", Digest: "root-digest", Title: "Root", Children: []string{"wiki/entities/one.md"}}}
	raw, err := EncodeSourceMaintenanceRequest(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Operation string `json:"operation"`
		Schema    string `json:"required_schema_digest"`
		SourceRef string `json:"required_source_ref"`
		Input     struct {
			SourceText  string                       `json:"source_text"`
			Schema      knowl.SchemaDocument         `json:"schema"`
			InputLimits knowl.MaintenanceInputLimits `json:"input_limits"`
			Pages       []map[string]json.RawMessage `json:"pages"`
		} `json:"input"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Operation != "source_maintenance" || envelope.SourceRef != SourceRefKey(input.Source) || envelope.Schema != input.Schema.Digest || envelope.Input.SourceText != input.SourceText || !reflect.DeepEqual(envelope.Input.Schema, input.Schema) {
		t.Fatal("lost essential source/schema identity")
	}
	if envelope.Input.InputLimits.MaxRequestBytes != MaxMaintenanceRequestBytes {
		t.Fatal("missing effective default limit")
	}
	fields := envelope.Input.Pages[0]
	if _, found := fields["body"]; found {
		t.Fatal("wire duplicates full Content with Body")
	}
	var page knowl.PageSnapshot
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &page); err != nil {
		t.Fatal(err)
	}
	want := input.Pages[0]
	want.Body = ""
	if !reflect.DeepEqual(page, want) {
		t.Fatalf("wire page=%#v want=%#v", page, want)
	}
	var complete struct {
		Input knowl.MaintenanceInput `json:"input"`
	}
	if err := json.Unmarshal(raw, &complete); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(complete.Input.Source, input.Source) || !reflect.DeepEqual(complete.Input.Catalogs, input.Catalogs) {
		t.Fatal("lost source provenance/catalog graph")
	}
	if input.Pages[0].Body != "Authoritative body" {
		t.Fatal("mutated application snapshot")
	}
}
func TestSourceRequestEncodingBudgetAndCancellation(t *testing.T) {
	input := requestTestInput()
	// Fixed-width decimal limits avoid changing serialized size at the boundary.
	input.SourceText = strings.Repeat("界<>\"\\\n", 2000)
	raw, err := EncodeSourceMaintenanceRequest(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.InputLimits.MaxRequestBytes = len(raw)
	raw, err = EncodeSourceMaintenanceRequest(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.InputLimits.MaxRequestBytes = len(raw)
	raw, err = EncodeSourceMaintenanceRequest(t.Context(), input)
	if err != nil || len(raw) != input.InputLimits.MaxRequestBytes {
		t.Fatalf("exact len=%d limit=%d err=%v", len(raw), input.InputLimits.MaxRequestBytes, err)
	}
	input.InputLimits.MaxRequestBytes--
	if _, err := EncodeSourceMaintenanceRequest(t.Context(), input); !errors.Is(err, ErrMaintenanceInputLimit) {
		t.Fatalf("overflow=%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := EncodeSourceMaintenanceRequest(ctx, requestTestInput()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}
func TestSourceRequestPreflightRejectsOversizedAndRecursiveData(t *testing.T) {
	input := requestTestInput()
	input.SourceText = strings.Repeat("x", MaxMaintenanceRequestBytes+1)
	if _, err := EncodeSourceMaintenanceRequest(t.Context(), input); !errors.Is(err, ErrMaintenanceInputLimit) {
		t.Fatalf("large source=%v", err)
	}
	input = requestTestInput()
	values := map[string]any{}
	values["cycle"] = values
	input.Pages[0].OKF = &okf.Metadata{Type: requestMetadataType, Extensions: values}
	if _, err := EncodeSourceMaintenanceRequest(t.Context(), input); !errors.Is(err, ErrMaintenanceInputInvalid) {
		t.Fatalf("cycle=%v", err)
	}
}
func requestTestInput() knowl.MaintenanceInput {
	return knowl.MaintenanceInput{ContractVersion: SourceMaintenanceContractVersion, Scope: fixtureScope, Schema: knowl.SchemaDocument{Digest: "schema", Content: []byte("# Schema\n界<>")}, Source: knowl.AcceptedSource{Source: knowl.SourceRef{Adapter: "fixture", ID: "source<>"}, Version: knowl.SourceVersion{Version: "1"}}, SourceText: "source 界<>\"", Pages: []knowl.PageSnapshot{{ID: "entities/one", Path: "wiki/entities/one.md", Digest: "digest", Title: "One", Content: "---\ntype: topic\n---\nAuthoritative body", Body: "Authoritative body", SourceRefs: []string{"fixture:original@1"}, Untrusted: true}}}
}

func TestSourceRequestPreservesOKFTimeMetadata(t *testing.T) {
	for _, populated := range []bool{false, true} {
		input := requestTestInput()
		input.Pages[0].OKF = &okf.Metadata{Type: requestMetadataType, Tags: []string{"storage"}}
		if populated {
			at := time.Date(2026, 10, 2, 1, 2, 3, 0, time.UTC)
			input.Pages[0].OKF.StaleAfter = &at
			input.Pages[0].OKF.Generated = &okf.Generation{By: "machine:fixture", At: &at}
		}
		raw, err := EncodeSourceMaintenanceRequest(t.Context(), input)
		if err != nil {
			t.Fatalf("populated=%v encode=%v", populated, err)
		}
		var envelope struct {
			Input struct {
				Pages []knowl.PageSnapshot `json:"pages"`
			} `json:"input"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(envelope.Input.Pages[0].OKF, input.Pages[0].OKF) {
			t.Fatal("lost OKF time metadata")
		}
	}
}

func TestSourceRequestRejectsCustomExtensionMarshalers(t *testing.T) {
	calls := 0
	for _, value := range []any{&requestJSONValue{Calls: &calls}, []requestJSONValue{{Calls: &calls}}, &requestTextValue{Calls: &calls}, []requestTextValue{{Calls: &calls}}} {
		input := requestTestInput()
		input.Pages[0].OKF = &okf.Metadata{Type: requestMetadataType, Extensions: map[string]any{requestExtensionKey: value}}
		if _, err := EncodeSourceMaintenanceRequest(t.Context(), input); !errors.Is(err, ErrMaintenanceInputInvalid) {
			t.Fatalf("type %T error=%v", value, err)
		}
		if calls != 0 {
			t.Fatal("called arbitrary extension marshaler")
		}
	}
}

type requestJSONValue struct{ Calls *int }

func (v *requestJSONValue) MarshalJSON() ([]byte, error) { *v.Calls++; return []byte("null"), nil }

type requestTextValue struct{ Calls *int }

func (v *requestTextValue) MarshalText() ([]byte, error) { *v.Calls++; return []byte("custom"), nil }

var requestByteMarshalCalls int

type requestJSONByte uint8

func (*requestJSONByte) MarshalJSON() ([]byte, error) {
	requestByteMarshalCalls++
	return []byte("1"), nil
}

type requestTextByte uint8

func (*requestTextByte) MarshalText() ([]byte, error) {
	requestByteMarshalCalls++
	return []byte("custom"), nil
}

func TestSourceRequestRejectsNamedByteMarshalers(t *testing.T) {
	requestByteMarshalCalls = 0
	for _, value := range []any{[]requestJSONByte{1}, &[1]requestJSONByte{1}, []requestTextByte{1}, &[1]requestTextByte{1}} {
		input := requestTestInput()
		input.Pages[0].OKF = &okf.Metadata{Type: requestMetadataType, Extensions: map[string]any{requestExtensionKey: value}}
		if _, err := EncodeSourceMaintenanceRequest(t.Context(), input); !errors.Is(err, ErrMaintenanceInputInvalid) {
			t.Fatalf("type %T error=%v calls=%d", value, err, requestByteMarshalCalls)
		}
		if requestByteMarshalCalls != 0 {
			t.Fatal("invoked named byte marshaler")
		}
	}
}

type requestHiddenFields struct{ Value any }
type requestOuterFields struct{ requestHiddenFields }

func TestSourceRequestRejectsUnsupportedExtensionStructs(t *testing.T) {
	calls := 0
	for _, value := range []any{requestOuterFields{requestHiddenFields{Value: &requestTextValue{Calls: &calls}}}, requestOuterFields{requestHiddenFields{Value: strings.Repeat("x", MaxMaintenanceRequestBytes+1)}}} {
		input := requestTestInput()
		input.Pages[0].OKF = &okf.Metadata{Type: requestMetadataType, Extensions: map[string]any{requestExtensionKey: value}}
		if _, err := EncodeSourceMaintenanceRequest(t.Context(), input); !errors.Is(err, ErrMaintenanceInputInvalid) {
			t.Fatalf("extension struct error=%v calls=%d", err, calls)
		}
		if calls != 0 {
			t.Fatal("called promoted extension marshaler")
		}
	}
}
