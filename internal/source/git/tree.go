package git

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/gobwas/glob"
)

const (
	pageTokenVersion = 1
	lfsPointerPrefix = "version https://git-lfs.github.com/spec/v1"
)

// Limits bounds tree traversal, document counts, and pagination for Git sources.
type Limits struct {
	PageSize     int
	MaxVisited   int
	MaxDocuments int
	MaxFileBytes int64
}

// DefaultLimits returns the default operational bounds for Git sources.
func DefaultLimits() Limits {
	return Limits{
		PageSize:     256,
		MaxVisited:   100_000,
		MaxDocuments: 50_000,
		MaxFileBytes: 64 << 20,
	}
}

type pageCursor struct {
	Version  int    `json:"v"`
	Snapshot string `json:"s"`
	After    string `json:"a"`
}

func encodePageToken(cursor pageCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

// EncodePageTokenForTest exposes token encoding for tests.
func EncodePageTokenForTest(version int, snapshot, after string) (string, error) {
	return encodePageToken(pageCursor{
		Version:  version,
		Snapshot: snapshot,
		After:    after,
	})
}

func decodePageToken(token string) (*pageCursor, error) {
	if strings.TrimSpace(token) == "" {
		return nil, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, WrapClassified(ClassScanInvalid, ErrPageToken, "invalid page token encoding")
	}
	var cursor pageCursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return nil, WrapClassified(ClassScanInvalid, ErrPageToken, "invalid page token payload")
	}
	if cursor.Version != pageTokenVersion || cursor.Snapshot == "" {
		return nil, WrapClassified(ClassScanInvalid, ErrPageToken, "unsupported or incomplete page token")
	}
	return &cursor, nil
}

// CompileMatchers compiles include patterns into slash-aware glob matchers.
func CompileMatchers(includes []string) ([]glob.Glob, error) {
	if len(includes) == 0 {
		includes = []string{"**/*.md"}
	}
	patterns := make([]string, 0, len(includes))
	for _, pattern := range includes {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		patterns = append(patterns, pattern)
	}

	for index := 0; index < len(patterns); index++ {
		current := patterns[index]
		position := strings.Index(current, "**/")
		if position < 0 {
			continue
		}
		withoutDirectory := current[:position] + current[position+3:]
		if !containsString(patterns, withoutDirectory) {
			patterns = append(patterns, withoutDirectory)
		}
	}

	matchers := make([]glob.Glob, 0, len(patterns))
	for _, candidate := range patterns {
		compiled, err := glob.Compile(candidate, '/')
		if err != nil {
			return nil, fmt.Errorf("compile include glob %q: %w", candidate, err)
		}
		matchers = append(matchers, compiled)
	}
	return matchers, nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func matchesAny(matchers []glob.Glob, p string) bool {
	if len(matchers) == 0 {
		return true
	}
	for _, m := range matchers {
		if m.Match(p) {
			return true
		}
	}
	return false
}

func isLFSPointer(f *object.File) bool {
	if f.Size <= 0 || f.Size > 1024 {
		return false
	}
	r, err := f.Reader()
	if err != nil {
		return false
	}
	defer func() {
		_ = r.Close()
	}()

	buf := make([]byte, len(lfsPointerPrefix))
	n, err := io.ReadFull(r, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return false
	}
	return string(buf[:n]) == lfsPointerPrefix
}

// TreeWalker walks Git object trees at a specified commit snapshot and filters descriptors.
type TreeWalker struct {
	limits Limits
}

// NewTreeWalker constructs a TreeWalker with bounded limits.
func NewTreeWalker(limits Limits) *TreeWalker {
	return &TreeWalker{limits: limits}
}

// Walk traverses the Git tree at snapshotHash, filtering entries and returning sorted DocumentRef descriptors.
func (w *TreeWalker) Walk(ctx context.Context, storer storage.Storer, snapshotHash plumbing.Hash, matchers []glob.Glob) ([]knowl.DocumentRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	commit, err := object.GetCommit(storer, snapshotHash)
	if err != nil {
		return nil, WrapClassified(ClassScanInvalid, err, fmt.Sprintf("commit snapshot %s not found in repository", snapshotHash.String()))
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, WrapClassified(ClassScanInvalid, err, fmt.Sprintf("tree for snapshot %s not found", snapshotHash.String()))
	}

	visited := 0
	var documents []knowl.DocumentRef
	snapshotSHA := snapshotHash.String()
	type pendingTree struct {
		tree   *object.Tree
		prefix string
	}
	pending := []pendingTree{{tree: tree}}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for _, entry := range current.tree.Entries {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			visited++
			if visited > w.limits.MaxVisited {
				return nil, WrapClassified(ClassResourceLimit, ErrLimit,
					fmt.Sprintf("visited tree entries exceeded limit of %d", w.limits.MaxVisited))
			}

			entryPath := path.Join(current.prefix, entry.Name)
			if entry.Mode == filemode.Dir {
				child, childErr := object.GetTree(storer, entry.Hash)
				if childErr != nil {
					return nil, WrapClassified(ClassScanInvalid, childErr, fmt.Sprintf("tree entry %s is unavailable", entryPath))
				}
				pending = append(pending, pendingTree{tree: child, prefix: entryPath})
				continue
			}
			if !entry.Mode.IsFile() || entry.Mode == filemode.Symlink || entry.Mode == filemode.Submodule || !matchesAny(matchers, entryPath) {
				continue
			}
			blob, blobErr := object.GetBlob(storer, entry.Hash)
			if blobErr != nil {
				return nil, WrapClassified(ClassScanInvalid, blobErr, fmt.Sprintf("blob entry %s is unavailable", entryPath))
			}
			file := object.NewFile(entryPath, entry.Mode, blob)
			if isLFSPointer(file) {
				continue
			}
			if len(documents)+1 > w.limits.MaxDocuments {
				return nil, WrapClassified(ClassResourceLimit, ErrLimit,
					fmt.Sprintf("matched documents exceeded limit of %d", w.limits.MaxDocuments))
			}
			blobSHA := entry.Hash.String()
			documents = append(documents, knowl.DocumentRef{
				ExternalID: knowl.DocumentID(entryPath), Path: entryPath, Revision: blobSHA,
				Metadata: map[string]string{"snapshot": snapshotSHA, "blob_sha": blobSHA},
			})
		}
	}

	// Deterministic lexicographical sorting by path
	sort.Slice(documents, func(i, j int) bool {
		return documents[i].Path < documents[j].Path
	})

	return documents, nil
}
