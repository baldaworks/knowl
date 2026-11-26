package searchtome

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPublishCanonicalPages(t *testing.T) {
	records := []CorpusRecord{{Title: "Second", Content: "Second source body."}, {Title: "First", Content: "First source body."}}
	left, err := Publish(t.Context(), filepath.Join(t.TempDir(), "workspace"), "education", records)
	if err != nil {
		t.Fatal(err)
	}
	right, err := Publish(t.Context(), filepath.Join(t.TempDir(), "workspace"), "education", []CorpusRecord{records[1], records[0]})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left.Titles, right.Titles) || len(left.Snapshot.Pages) != 2 {
		t.Fatalf("inconsistent page mapping: %v %v", left.Titles, right.Titles)
	}
	for i, p := range left.Snapshot.Pages {
		q := right.Snapshot.Pages[i]
		if p.ID != q.ID || p.Content != q.Content || left.Titles[p.ID] != p.Title {
			t.Fatalf("page %d differs", i)
		}
	}
	other, err := Publish(t.Context(), filepath.Join(t.TempDir(), "workspace"), "finance", records)
	if err != nil {
		t.Fatal(err)
	}
	for id := range left.Titles {
		if _, ok := other.Titles[id]; ok {
			t.Fatal("page IDs crossed domains")
		}
	}
	_, err = Publish(t.Context(), filepath.Join(t.TempDir(), "workspace"), "education", append(records, records[0]))
	if !errors.Is(err, ErrPublication) {
		t.Fatalf("duplicate error: %v", err)
	}
}

func TestPublishRejectsNonemptyWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("user"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Publish(context.Background(), dir, "education", nil)
	if !errors.Is(err, ErrPublication) {
		t.Fatalf("error: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "keep"))
	if err != nil || string(content) != "user" {
		t.Fatalf("workspace modified: %v", err)
	}
}
