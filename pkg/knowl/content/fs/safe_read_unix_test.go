//go:build linux || darwin

package fs

import (
	"errors"
	"path/filepath"
	"testing"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"golang.org/x/sys/unix"
)

func TestReadRootRejectsFIFOWithoutBlocking(t *testing.T) {
	directory := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(directory, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := openReadRoot(directory, "")
	if err != nil {
		t.Fatal(err)
	}
	defer root.close()
	if _, _, err := root.read("pipe", knowl.ReadLimits{}, 100); !errors.Is(err, ErrPathRejected) {
		t.Fatalf("FIFO read = %v", err)
	}
}
