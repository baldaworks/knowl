package fs

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

// readRoot pins a directory for a locked canonical read. Every child open is
// descriptor-relative and rejects symlinks, including links within this root.
// The configured workspace's parent directories are trusted host configuration.
type readRoot struct{ file *os.File }

func openReadRoot(workspacePath, relative string) (*readRoot, error) {
	file, err := openReadDirectory(workspacePath)
	if err != nil {
		return nil, err
	}
	root := &readRoot{file: file}
	if relative == "" {
		return root, nil
	}
	child, err := root.open(relative, true)
	root.close()
	if err != nil {
		return nil, err
	}
	return &readRoot{file: child}, nil
}

func (root *readRoot) close() { _ = root.file.Close() }

func validReadPath(relative string) bool {
	return app.ValidOperatorReadPath(relative)
}

func (root *readRoot) open(relative string, directory bool) (*os.File, error) {
	if !validReadPath(relative) {
		return nil, ErrPathRejected
	}
	parent := root.file
	var owned *os.File
	defer func() {
		if owned != nil {
			_ = owned.Close()
		}
	}()
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		last := index == len(parts)-1
		child, err := openReadChild(parent, part, !last || directory)
		if err != nil {
			return nil, fmt.Errorf("open canonical path %q: %w", relative, err)
		}
		if last {
			return child, nil
		}
		if owned != nil {
			_ = owned.Close()
		}
		owned = child
		parent = child
	}
	return nil, ErrPathRejected
}

// read bounds allocation even if the file grows after Stat, and returns metadata
// from the same descriptor as the bytes, rejecting observed size/mtime changes.
func (root *readRoot) read(relative string, limits knowl.ReadLimits, ceiling int) ([]byte, os.FileInfo, error) {
	file, err := root.open(relative, false)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, ErrPathRejected
	}
	bound := ceiling
	if limits.Bytes > 0 && limits.Bytes < bound {
		bound = limits.Bytes
	}
	if limits.Characters > 0 && limits.Characters <= bound/utf8.UTFMax {
		bound = limits.Characters * utf8.UTFMax
	}
	if bound <= 0 || info.Size() > int64(bound) {
		return nil, nil, app.ErrOperatorReadLimitExceeded
	}
	readBound := int64(bound)
	if readBound < math.MaxInt64 {
		readBound++
	}
	content, err := io.ReadAll(io.LimitReader(file, readBound))
	if err != nil {
		return nil, nil, err
	}
	if len(content) > bound || (limits.Characters > 0 && utf8.RuneCount(content) > limits.Characters) {
		return nil, nil, app.ErrOperatorReadLimitExceeded
	}
	after, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) || int64(len(content)) != after.Size() {
		return nil, nil, app.ErrOperatorWorkspaceUnavailable
	}
	return content, after, nil
}

// walk streams directory entries from pinned descriptors, without following any
// directory links or allocating an unbounded directory inventory.
func (root *readRoot) walk(visit func(string, os.DirEntry) error) error {
	count := 0
	var walkDirectory func(*os.File, string) error
	walkDirectory = func(directory *os.File, prefix string) error {
		for {
			entries, err := directory.ReadDir(128)
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			for _, entry := range entries {
				count++
				if count > maxHierarchySnapshotEntries {
					return app.ErrOperatorReadLimitExceeded
				}
				relative := path.Join(prefix, entry.Name())
				if !validReadPath(relative) || entry.Type()&os.ModeSymlink != 0 {
					return ErrPathRejected
				}
				if err := visit(relative, entry); err != nil {
					return err
				}
				if entry.IsDir() {
					child, err := openReadChild(directory, entry.Name(), true)
					if err != nil {
						return err
					}
					err = walkDirectory(child, relative)
					_ = child.Close()
					if err != nil {
						return err
					}
				}
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
		}
	}
	return walkDirectory(root.file, "")
}
