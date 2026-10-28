package searchtome

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadDataset(t *testing.T) {
	root := t.TempDir()
	corpus := []byte("{\"title\":\"Document\",\"content\":\"body\"}\n")
	query := []byte("{\"query_id\":\"q1\",\"query\":\"find\",\"answer\":\"body\",\"gold_doc_title\":\"Document\"}\n")
	for _, domain := range Domains {
		if err := os.Mkdir(filepath.Join(root, domain), 0700); err != nil {
			t.Fatal(err)
		}
		for _, split := range []string{splitCorpus, splitTrain, splitDev, splitTest} {
			b := query
			if split == splitCorpus {
				b = corpus
			}
			if err := os.WriteFile(filepath.Join(root, domain, split+".jsonl"), b, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := LoadDataset(root); !errors.Is(err, ErrDataset) {
		t.Fatalf("unverified revision: %v", err)
	}
	d, err := loadDataset(root, false)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(corpus)
	if d.Revision != "fixture" || len(d.Files) != 24 || d.Files[0].Count != 1 || d.Files[0].SHA256 != hex.EncodeToString(sum[:]) || !reflect.DeepEqual(d.Files[0].Fields, []string{"content", "title"}) {
		t.Fatalf("invalid manifest: %+v", d.Files[0])
	}
	if len(d.Corpus["education"]) != 1 || len(d.Queries["education"]) != 1 || d.Queries["education"][0].Gold != "Document" {
		t.Fatal("invalid loaded records")
	}
	testPath := filepath.Join(root, "education", "test.jsonl")
	duplicate := append(append([]byte{}, query...), query...)
	if err := os.WriteFile(testPath, duplicate, 0600); err != nil {
		t.Fatal(err)
	}
	repeated, err := loadDataset(root, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range repeated.Queries["education"] {
		if q.Exclusion != exDuplicate {
			t.Fatalf("duplicate exclusion: %+v", q)
		}
	}
	collision := []byte("{\"query_id\":\"education/test/2\",\"query\":\"x\",\"gold_doc_title\":\"Document\"}\n{\"query\":\"x\",\"gold_doc_title\":\"Document\"}\n")
	if err := os.WriteFile(testPath, collision, 0600); err != nil {
		t.Fatal(err)
	}
	repeated, err = loadDataset(root, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range repeated.Queries["education"] {
		if q.Exclusion != exDuplicate {
			t.Fatalf("generated ID collision: %+v", q)
		}
	}
}

func TestGoldStatus(t *testing.T) {
	corpus := []CorpusRecord{{Title: testOne}, {Title: testDuplicate}, {Title: testDuplicate}}
	cases := []struct {
		q    Query
		want string
	}{{Query{Query: "x"}, "missing_gold"}, {Query{Query: "x", Gold: "unknown"}, exUnknownGold}, {Query{Query: "x", Gold: testDuplicate}, "ambiguous_gold"}, {Query{Gold: testOne}, "empty_query"}, {Query{Query: "x", Gold: testOne}, ""}}
	for _, c := range cases {
		if got := GoldStatus(c.q, corpus); got != c.want {
			t.Fatalf("GoldStatus(%+v) = %q, want %q", c.q, got, c.want)
		}
	}
}

func TestQueryNumericID(t *testing.T) {
	var q Query
	if err := json.Unmarshal([]byte(`{"query_id":123,"query":"a","gold_doc_title":"b"}`), &q); err != nil || q.ID != "123" {
		t.Fatalf("query: %+v, error %v", q, err)
	}
}
