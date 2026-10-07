package fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/types"
)

const workspaceLockHelperEnv = "KNOWL_TEST_WORKSPACE_LOCK_HELPER"

type lockHelperStderr struct {
	mu sync.Mutex
	bytes.Buffer
}

func (buffer *lockHelperStderr) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.Buffer.Write(data)
}

func (buffer *lockHelperStderr) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.Buffer.String()
}

func TestWorkspaceLockCoordinatesInstancesAndCancellation(t *testing.T) {
	root := t.TempDir()
	first, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Init(); err != nil {
		t.Fatal(err)
	}
	second, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	unlockFirst, err := first.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := second.lock(ctx); !errors.Is(err, ErrWorkspaceBusy) || !errors.Is(err, context.DeadlineExceeded) {
		unlockFirst()
		t.Fatalf("contended lock error = %v, want busy and deadline", err)
	}
	unlockFirst()

	unlockSecond, err := second.lock(context.Background())
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	unlockSecond()
}

func TestWorkspaceLocksAreIndependentByRoot(t *testing.T) {
	workspaces := make([]*Workspace, 0, 2)
	for range 2 {
		workspace, err := New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := workspace.Init(); err != nil {
			t.Fatal(err)
		}
		workspaces = append(workspaces, workspace)
	}

	unlockFirst, err := workspaces[0].lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlockFirst()
	unlockSecond, err := workspaces[1].lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	unlockSecond()
}

func TestWorkspaceReadWaitsForCanonicalTransition(t *testing.T) {
	root := t.TempDir()
	writer, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Init(); err != nil {
		t.Fatal(err)
	}
	reader, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	unlock, err := writer.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	onePath := filepath.Join(root, "wiki", "entities", "one.md")
	twoPath := filepath.Join(root, "wiki", "entities", "two.md")
	if err := writeAtomic(onePath, validWorkspacePage("entities/one", "One", testWorkspaceSourceRef, ""), 0o600); err != nil {
		unlock()
		t.Fatal(err)
	}

	type readResult struct {
		pages []knowl.PageSnapshot
		err   error
	}
	result := make(chan readResult, 1)
	go func() {
		pages, readErr := reader.ReadPages(context.Background(), testScope, []knowl.PageID{"entities/one", "entities/two"}, knowl.ReadLimits{})
		result <- readResult{pages: pages, err: readErr}
	}()
	select {
	case got := <-result:
		unlock()
		t.Fatalf("read completed during transition: %#v, %v", got.pages, got.err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := writeAtomic(twoPath, validWorkspacePage("entities/two", "Two", testWorkspaceSourceRef, ""), 0o600); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("read after transition: %v", got.err)
		}
		if len(got.pages) != 2 {
			t.Fatalf("read pages = %#v, want complete transition", got.pages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read did not resume after transition")
	}
}

func TestWorkspaceLockReleasedAfterProcessExit(t *testing.T) {
	root := t.TempDir()
	workspace, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.Init(); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	command := exec.Command(os.Args[0], "-test.run=^TestWorkspaceLockHelperProcess$")
	command.Env = append(os.Environ(), workspaceLockHelperEnv+"=1", "KNOWL_TEST_WORKSPACE_ROOT="+root, "KNOWL_TEST_WORKSPACE_READY="+ready)
	stdin, signal, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stdin = stdin
	var stderr lockHelperStderr
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = signal.Close()
		t.Fatal(err)
	}
	_ = stdin.Close()
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	childExited := false
	defer func() {
		_ = signal.Close()
		if !childExited {
			_ = command.Process.Kill()
			<-exited
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case exitErr := <-exited:
			childExited = true
			t.Fatalf("helper exited before acquiring lock: %v; stderr: %s", exitErr, stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper did not acquire lock: %s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case exitErr := <-exited:
		childExited = true
		t.Fatalf("helper exited after reporting ready: %v; stderr: %s", exitErr, stderr.String())
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	unexpectedUnlock, err := workspace.lock(ctx)
	if err == nil {
		unexpectedUnlock()
	}
	if !errors.Is(err, ErrWorkspaceBusy) {
		cancel()
		select {
		case exitErr := <-exited:
			childExited = true
			t.Fatalf("lock while helper is alive = %v, want busy; helper exit: %v; stderr: %s", err, exitErr, stderr.String())
		default:
			t.Fatalf("lock while helper is alive = %v, want busy; helper stderr: %s", err, stderr.String())
		}
	}
	cancel()
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := <-exited; err == nil {
		childExited = true
		t.Fatal("killed helper exited without an error")
	}
	childExited = true

	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	unlock, err := workspace.lock(ctx)
	if err != nil {
		t.Fatalf("lock after helper exit: %v; helper stderr: %s", err, stderr.String())
	}
	unlock()
}

func TestWorkspaceLockHelperProcess(t *testing.T) {
	if os.Getenv(workspaceLockHelperEnv) != "1" {
		return
	}
	workspace, err := New(os.Getenv("KNOWL_TEST_WORKSPACE_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := workspace.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := os.WriteFile(os.Getenv("KNOWL_TEST_WORKSPACE_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}
