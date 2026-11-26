package main

import (
	"os"
	"path/filepath"
	"testing"

	st "github.com/baldaworks/knowl/pkg/knowl/store/eval/searchtome"
)

func TestSafeDataset(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "dataset")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, d := range st.Domains {
		dir := filepath.Join(root, d)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{"corpus", "train", "dev", "test"} {
			if err := os.WriteFile(filepath.Join(dir, s+".jsonl"), []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := safeDataset(base, "dataset"); err != nil {
		t.Fatal(err)
	}
	if _, err := safeDataset(base, "../dataset"); err == nil {
		t.Fatal("accepted traversal")
	}
	p := filepath.Join(root, "education", "test.jsonl")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "outside"), p); err != nil {
		t.Fatal(err)
	}
	if _, err := safeDataset(base, "dataset"); err == nil {
		t.Fatal("accepted symlink")
	}
}
func TestOwnedBaseRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, ".searchtome-benchmark")); err != nil {
		t.Fatal(err)
	}
	if _, err := ownedBase(root); err == nil {
		t.Fatal("accepted symlink base")
	}
}
