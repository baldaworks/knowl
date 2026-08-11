package bootstrap

import (
	"fmt"
	"path/filepath"
	"strings"
)

const (
	indexFile        = "wiki/index.md"
	logFile          = "wiki/log.md"
	workspaceWikiDir = "wiki"
)

func ensurePathsAreSeparate(workspaceRoot, sourceRoot string) error {
	overlap, err := pathsOverlap(workspaceRoot, sourceRoot)
	if err != nil {
		return err
	}
	if overlap {
		return fmt.Errorf("bootstrap source path %q must be separate from workspace %q", sourceRoot, workspaceRoot)
	}
	return nil
}

func referenceKey(path string) string {
	normalized := filepath.ToSlash(strings.TrimSpace(path))
	normalized = strings.TrimPrefix(normalized, "./")
	normalized = trimMarkdownExtension(normalized)
	return normalized
}

func trimMarkdownExtension(path string) string {
	if strings.EqualFold(filepath.Ext(path), ".md") {
		return path[:len(path)-len(filepath.Ext(path))]
	}
	return path
}

func pathsOverlap(left, right string) (bool, error) {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if left == right {
		return true, nil
	}
	isWithin := func(root, child string) (bool, error) {
		relative, err := filepath.Rel(root, child)
		if err != nil {
			return false, err
		}
		return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
	}
	leftContainsRight, err := isWithin(left, right)
	if err != nil {
		return false, err
	}
	rightContainsLeft, err := isWithin(right, left)
	if err != nil {
		return false, err
	}
	return leftContainsRight || rightContainsLeft, nil
}
