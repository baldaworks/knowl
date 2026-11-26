package searchtome

import (
	"encoding/json"
	"errors"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func validReport() Report {
	d := Dataset{Revision: Revision, Corpus: map[string][]CorpusRecord{}, Queries: map[string][]Query{}}
	projections := map[string]map[string]Projection{}
	var l, h []Observation
	for _, domain := range Domains {
		d.Corpus[domain] = []CorpusRecord{{Title: testGold, Content: "body"}}
		d.Queries[domain] = []Query{{ID: "1", Query: "query", Gold: testGold}}
		for _, split := range []string{splitCorpus, splitTrain, splitDev, splitTest} {
			d.Files = append(d.Files, FileManifest{Split: split, Count: 1})
		}
		projections[domain] = map[string]Projection{modeLexical: {Pages: 1}, modeHybrid: {Pages: 1}}
		l = append(l, observation(domain, "1", 1, knowl.RetrievalLexical, 10))
		h = append(h, observation(domain, "1", 1, knowl.RetrievalHybrid, 20))
	}
	truncate := false
	env := Environment{TEI: TEIInfo{Model: ReferenceSpace.Model, Revision: ReferenceSpace.Revision, Version: teiVersion, DType: teiFloat32, AutoTruncate: &truncate}, Commit: "abc", TreeState: "clean", GoVersion: "go1.26", OS: "linux", Arch: "amd64", CPU: "fixture-cpu", Backend: "sqlite", Model: ReferenceSpace, FailurePolicy: modeLexical, K: 3, EmbeddingBatchInputs: 4, Characters: 1024, SourceFilter: "all", RunKind: runFull, Warmup: "none", Commands: []string{"benchmark"}}
	for i := range l {
		l[i].Pages = []ResultPage{{ID: "id", Title: testGold}}
		h[i].Pages = []ResultPage{{ID: "id", Title: testGold}}
	}
	return NewReport(env, d, projections, l, h)
}
func TestReportDeterminismAndCompletion(t *testing.T) {
	report := validReport()
	a, b := t.TempDir(), t.TempDir()
	if err := WriteReport(a, report); err != nil {
		t.Fatal(err)
	}
	if err := WriteReport(b, report); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{reportJSON, reportMarkdown, "complete.json"} {
		first, err := os.ReadFile(filepath.Join(a, name))
		if err != nil {
			t.Fatal(err)
		}
		second, err := os.ReadFile(filepath.Join(b, name))
		if err != nil || !reflect.DeepEqual(first, second) {
			t.Fatalf("nondeterministic %s", name)
		}
	}
	data, err := os.ReadFile(filepath.Join(a, reportJSON))
	if err != nil {
		t.Fatal(err)
	}
	var got Report
	if err = json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Paired.Count != 6 || got.Lexical[0].Rank != 1 {
		t.Fatalf("roundtrip: %+v", got.Paired)
	}
	var marker map[string]string
	bytes, err := os.ReadFile(filepath.Join(a, "complete.json"))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(bytes, &marker) != nil || marker[reportJSON] != hashHex(data) {
		t.Fatal("invalid completion marker")
	}
	if WriteReport(a, report) == nil {
		t.Fatal("overwrote completed report")
	}
}
func TestInterruptedReportHasNoCompletionMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, reportMarkdown), 0700); err != nil {
		t.Fatal(err)
	}
	if WriteReport(dir, validReport()) == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(filepath.Join(dir, "complete.json")); !os.IsNotExist(err) {
		t.Fatalf("completion marker exists: %v", err)
	}
}

func TestIncompleteReportCannotComplete(t *testing.T) {
	dir := t.TempDir()
	report := validReport()
	report.Hybrid = report.Hybrid[:len(report.Hybrid)-1]
	if err := WriteReport(dir, report); err == nil {
		t.Fatal("accepted missing outcome")
	}
	if _, err := os.Stat(filepath.Join(dir, "complete.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected marker: %v", err)
	}
}

func TestWriteFailureAfterJSONCannotComplete(t *testing.T) {
	dir := t.TempDir()
	r := validReport()
	fail := func(dir, name string, data []byte) error {
		if name == reportMarkdown {
			return errors.New("injected failure")
		}
		return atomicFile(dir, name, data)
	}
	if err := writeReport(dir, r, fail); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(filepath.Join(dir, reportJSON)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "complete.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected marker: %v", err)
	}
}
