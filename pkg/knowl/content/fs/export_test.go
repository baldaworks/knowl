package fs

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExportOKF(t *testing.T) {
	workspace := newExportWorkspace(t)
	writeExportFixture(t, workspace, "wiki/assets/logo.bin", []byte{0, 1, 255})
	writeExportFixture(t, workspace, "raw/private.txt", []byte("secret"))
	destination := filepath.Join(t.TempDir(), "public")
	if err := workspace.ExportOKF(context.Background(), destination, DefaultExportLimits()); err != nil {
		t.Fatal(err)
	}
	if got, want := treeDigest(t, destination), treeDigest(t, filepath.Join(workspace.Root(), "wiki")); !reflect.DeepEqual(got, want) {
		t.Fatalf("export = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(filepath.Join(destination, "raw")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("raw sibling exported: %v", err)
	}
}

func TestExportOKFRejectsUnsafeInput(t *testing.T) {
	workspace := newExportWorkspace(t)
	if err := workspace.ExportOKF(context.Background(), t.TempDir(), DefaultExportLimits()); !errors.Is(err, ErrExportDestinationExists) {
		t.Fatalf("existing destination: %v", err)
	}
	if err := workspace.ExportOKF(context.Background(), filepath.Join(workspace.Root(), "wiki", "copy"), DefaultExportLimits()); !errors.Is(err, ErrPathRejected) {
		t.Fatalf("nested destination: %v", err)
	}
	if err := os.Symlink("index.md", filepath.Join(workspace.Root(), "wiki", "alias.md")); err != nil {
		t.Fatal(err)
	}
	if err := workspace.ExportOKF(context.Background(), filepath.Join(t.TempDir(), "copy"), DefaultExportLimits()); !errors.Is(err, ErrPathRejected) {
		t.Fatalf("symlink: %v", err)
	}

	workspace = newExportWorkspace(t)
	listener, err := net.Listen("unix", filepath.Join(workspace.Root(), "wiki", "socket"))
	if err != nil {
		t.Skipf("Unix socket unavailable: %v", err)
	}
	defer func() { _ = listener.Close() }()
	if err := workspace.ExportOKF(context.Background(), filepath.Join(t.TempDir(), "special"), DefaultExportLimits()); !errors.Is(err, ErrPathRejected) {
		t.Fatalf("special file: %v", err)
	}
}

func TestExportOKFFailsOnSourceChange(t *testing.T) {
	workspace := newExportWorkspace(t)
	workspace.exportFault = func(string) error {
		return os.WriteFile(filepath.Join(workspace.Root(), "wiki", "index.md"), []byte("changed"), 0o600)
	}
	destination := filepath.Join(t.TempDir(), "public")
	if err := workspace.ExportOKF(context.Background(), destination, DefaultExportLimits()); !errors.Is(err, ErrExportSourceChanged) {
		t.Fatalf("source change: %v", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial destination: %v", err)
	}
}

func TestExportOKFHonorsCancellationAndLimits(t *testing.T) {
	workspace := newExportWorkspace(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := workspace.ExportOKF(ctx, filepath.Join(t.TempDir(), "cancelled"), DefaultExportLimits()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	limits := DefaultExportLimits()
	limits.MaxFiles = 1
	if err := workspace.ExportOKF(context.Background(), filepath.Join(t.TempDir(), "limited"), limits); !errors.Is(err, ErrExportLimitExceeded) {
		t.Fatalf("limit: %v", err)
	}
	limits = DefaultExportLimits()
	limits.MaxFileBytes = 4
	if err := workspace.ExportOKF(context.Background(), filepath.Join(t.TempDir(), "oversized"), limits); !errors.Is(err, ErrExportLimitExceeded) {
		t.Fatalf("file byte limit: %v", err)
	}
}

func newExportWorkspace(t *testing.T) *Workspace {
	t.Helper()
	workspace, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	return workspace
}

func writeExportFixture(t *testing.T, workspace *Workspace, relative string, data []byte) {
	t.Helper()
	path := filepath.Join(workspace.Root(), filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func treeDigest(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil {
			result[filepath.ToSlash(relative)] = digestBytes(data)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
