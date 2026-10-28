// Package searchtome prepares isolated SearchTome benchmark corpora.
package searchtome

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const Revision = "48d3b3545950b3857dc3657fb22e4b79b8ed8df5"

var Domains = []string{"education", "finance", "law", "medicine", "natural_sciences", "social_sciences"}

type CorpusRecord struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}
type Query struct {
	ID        string `json:"query_id"`
	Query     string `json:"query"`
	Answer    string `json:"answer"`
	Gold      string `json:"gold_doc_title"`
	Exclusion string `json:"exclusion,omitempty"`
}

// UnmarshalJSON accepts SearchTome integer identifiers and string IDs.
func (q *Query) UnmarshalJSON(data []byte) error {
	var v struct {
		ID     json.RawMessage `json:"query_id"`
		Query  string          `json:"query"`
		Answer string          `json:"answer"`
		Gold   string          `json:"gold_doc_title"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	q.Query = v.Query
	q.Answer = v.Answer
	q.Gold = v.Gold
	if len(v.ID) == 0 || string(v.ID) == "null" {
		return nil
	}
	if v.ID[0] == '"' {
		return json.Unmarshal(v.ID, &q.ID)
	}
	var n json.Number
	if err := json.Unmarshal(v.ID, &n); err != nil {
		return err
	}
	if _, err := n.Int64(); err != nil {
		return err
	}
	q.ID = n.String()
	return nil
}

type FileManifest struct {
	Configuration string              `json:"configuration"`
	Split         string              `json:"split"`
	Path          string              `json:"path"`
	SHA256        string              `json:"sha256"`
	Count         int                 `json:"count"`
	Fields        []string            `json:"fields"`
	FieldTypes    map[string][]string `json:"field_types"`
}
type Dataset struct {
	Revision string                    `json:"revision"`
	Files    []FileManifest            `json:"files"`
	Corpus   map[string][]CorpusRecord `json:"-"`
	Queries  map[string][]Query        `json:"-"`
}

var ErrDataset = errors.New("invalid SearchTome dataset")

//go:embed pinned_manifest.json
var pinnedManifest []byte

type pinnedFile struct {
	SHA256 string `json:"sha256"`
	Count  int    `json:"count"`
}

// LoadDataset verifies the pinned files and retains only test queries.
func LoadDataset(root string) (Dataset, error) { return loadDataset(root, true) }
func loadDataset(root string, verify bool) (Dataset, error) {
	var pinned map[string]pinnedFile
	if err := json.Unmarshal(pinnedManifest, &pinned); err != nil {
		return Dataset{}, err
	}
	revision := "fixture"
	if verify {
		revision = Revision
	}
	d := Dataset{Revision: revision, Corpus: map[string][]CorpusRecord{}, Queries: map[string][]Query{}}
	for _, domain := range Domains {
		for _, split := range []string{splitCorpus, splitTrain, splitDev, splitTest} {
			path := filepath.Join(domain, split+".jsonl")
			b, err := os.ReadFile(filepath.Join(root, path))
			if err != nil {
				return Dataset{}, fmt.Errorf("read %s: %w", path, err)
			}
			sum := sha256.Sum256(b)
			config := domain + "_queries"
			if split == splitCorpus {
				config = domain + "_corpus"
			}
			m := FileManifest{Configuration: config, Split: split, Path: filepath.ToSlash(path), SHA256: hex.EncodeToString(sum[:])}
			scanner := bufio.NewScanner(bytes.NewReader(b))
			scanner.Buffer(make([]byte, 4096), 4<<20)
			seenFields := map[string]bool{}
			types := map[string]map[string]bool{}
			for scanner.Scan() {
				if len(scanner.Bytes()) == 0 {
					continue
				}
				var obj map[string]json.RawMessage
				if err := json.Unmarshal(scanner.Bytes(), &obj); err != nil {
					return Dataset{}, fmt.Errorf("%s record %d: %w", path, m.Count, ErrDataset)
				}
				for f, value := range obj {
					seenFields[f] = true
					var decoded any
					if err := json.Unmarshal(value, &decoded); err != nil {
						return Dataset{}, ErrDataset
					}
					name := fmt.Sprintf("%T", decoded)
					if types[f] == nil {
						types[f] = map[string]bool{}
					}
					types[f][name] = true
				}
				m.Count++
				if split == splitCorpus {
					var r CorpusRecord
					if json.Unmarshal(scanner.Bytes(), &r) != nil || r.Title == "" || r.Content == "" {
						return Dataset{}, fmt.Errorf("%s record %d: %w", path, m.Count, ErrDataset)
					}
					d.Corpus[domain] = append(d.Corpus[domain], r)
				} else {
					var q Query
					if json.Unmarshal(scanner.Bytes(), &q) != nil {
						return Dataset{}, fmt.Errorf("%s record %d: %w", path, m.Count, ErrDataset)
					}
					if q.ID == "" {
						q.ID = fmt.Sprintf("%s/%s/%d", domain, split, m.Count)
					}
					if split == splitTest {
						d.Queries[domain] = append(d.Queries[domain], q)
					}
				}
			}
			if err := scanner.Err(); err != nil {
				return Dataset{}, err
			}
			for field := range seenFields {
				m.Fields = append(m.Fields, field)
			}
			sort.Strings(m.Fields)
			m.FieldTypes = map[string][]string{}
			for field, kinds := range types {
				for kind := range kinds {
					m.FieldTypes[field] = append(m.FieldTypes[field], kind)
				}
				sort.Strings(m.FieldTypes[field])
			}
			if verify {
				expected, ok := pinned[m.Path]
				if !ok || expected.SHA256 != m.SHA256 || expected.Count != m.Count {
					return Dataset{}, fmt.Errorf("%s: %w", m.Path, ErrDataset)
				}
			}
			d.Files = append(d.Files, m)
		}
	}
	for domain, queries := range d.Queries {
		counts := map[string]int{}
		for _, q := range queries {
			counts[q.ID]++
		}
		for i := range queries {
			if counts[queries[i].ID] > 1 {
				queries[i].Exclusion = exDuplicate
			} else {
				queries[i].Exclusion = GoldStatus(queries[i], d.Corpus[domain])
			}
		}
		d.Queries[domain] = queries
	}
	return d, nil
}

// GoldStatus classifies a query before retrieval without guessing its target.
func GoldStatus(query Query, corpus []CorpusRecord) string {
	if query.Gold == "" {
		return "missing_gold"
	}
	count := 0
	for _, record := range corpus {
		if record.Title == query.Gold {
			count++
		}
	}
	if count == 0 {
		return exUnknownGold
	}
	if count > 1 {
		return "ambiguous_gold"
	}
	if query.Query == "" {
		return "empty_query"
	}
	return ""
}
