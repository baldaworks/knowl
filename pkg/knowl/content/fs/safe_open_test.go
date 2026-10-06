package fs

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

func TestReadRootPinsDirectoryAndRejectsLinks(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "wiki"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "wiki", "page.md"), []byte("published"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := openReadRoot(directory, "wiki")
	if err != nil {
		t.Fatal(err)
	}
	defer root.close()
	if err := os.Rename(filepath.Join(directory, "wiki"), filepath.Join(directory, "saved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "wiki"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "wiki", "page.md"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, _, err := root.read("page.md", knowl.ReadLimits{}, 100)
	if err != nil || string(content) != "published" {
		t.Fatalf("pinned read = %q, %v", content, err)
	}
	for _, target := range []string{filepath.Join(directory, "wiki", "page.md"), filepath.Join(directory, "saved", "page.md")} {
		link := filepath.Join(directory, "saved", "link.md")
		createReadTestSymlink(t, target, link)
		if _, _, err := root.read("link.md", knowl.ReadLimits{}, 100); !errors.Is(err, ErrPathRejected) {
			t.Fatalf("link read = %v", err)
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
	}
	createReadTestSymlink(t, filepath.Join(directory, "wiki"), filepath.Join(directory, "saved", "linked"))
	if _, _, err := root.read("linked/page.md", knowl.ReadLimits{}, 100); !errors.Is(err, ErrPathRejected) {
		t.Fatalf("linked directory read = %v", err)
	}
	if _, _, err := root.read("linked", knowl.ReadLimits{}, 100); !errors.Is(err, ErrPathRejected) {
		t.Fatalf("directory read = %v", err)
	}
}

func TestReadRootBoundsSparseFilesAndRunes(t *testing.T) {
	directory := t.TempDir()
	sparse, err := os.Create(filepath.Join(directory, "huge"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sparse.Truncate(1 << 30); err != nil {
		t.Fatal(err)
	}
	if err := sparse.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "unicode"), []byte("界界"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := openReadRoot(directory, "")
	if err != nil {
		t.Fatal(err)
	}
	defer root.close()
	if _, _, err := root.read("huge", knowl.ReadLimits{Bytes: 16}, 100); !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
		t.Fatalf("sparse read = %v", err)
	}
	if _, _, err := root.read("unicode", knowl.ReadLimits{Characters: 1}, 100); !errors.Is(err, app.ErrOperatorReadLimitExceeded) {
		t.Fatalf("rune limit = %v", err)
	}
	content, info, err := root.read("unicode", knowl.ReadLimits{Characters: 2}, 100)
	if err != nil || string(content) != "界界" || info.Size() != 6 {
		t.Fatalf("exact limit = %q, %#v, %v", content, info, err)
	}
	content, _, err = root.read("unicode", knowl.ReadLimits{}, math.MaxInt)
	if err != nil || string(content) != "界界" {
		t.Fatalf("maximum configured bound = %q, %v", content, err)
	}
}

func TestReadRootRejectsComponentReplacementRace(t *testing.T) {
	directory := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "page"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(directory, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "page"), []byte("published"), 0o600); err != nil {
		t.Fatal(err)
	}
	createReadTestSymlink(t, outside, filepath.Join(directory, "link"))
	root, err := openReadRoot(directory, "")
	if err != nil {
		t.Fatal(err)
	}
	defer root.close()
	done := make(chan error, 1)
	go func() {
		for range 500 {
			if err := os.Rename(child, filepath.Join(directory, "saved")); err != nil {
				done <- err
				return
			}
			if err := os.Rename(filepath.Join(directory, "link"), child); err != nil {
				done <- err
				return
			}
			runtime.Gosched()
			if err := os.Rename(child, filepath.Join(directory, "link")); err != nil {
				done <- err
				return
			}
			if err := os.Rename(filepath.Join(directory, "saved"), child); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for range 1000 {
		content, _, err := root.read("child/page", knowl.ReadLimits{}, 100)
		if err == nil && string(content) != "published" {
			t.Errorf("exposed substituted content: %q", content)
			break
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func createReadTestSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("security fixture requires native symlink creation: %v", err)
	}
}
