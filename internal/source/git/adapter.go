package git

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"gopkg.in/yaml.v3"
)

// RepositoryOpener provides access to a local Git repository for a configured source.
type RepositoryOpener interface {
	OpenOrClone(ctx context.Context, source knowl.Source) (*gogit.Repository, error)
}

// Adapter implements app.SourceAdapter for remote Git sources.
type Adapter struct {
	limits    Limits
	resolver  *RefResolver
	repoStore RepositoryOpener
	walker    *TreeWalker
}

// NewAdapter constructs a Git source adapter with explicit operational bounds.
func NewAdapter(limits Limits, resolver *RefResolver, repoStore RepositoryOpener) (*Adapter, error) {
	if limits.PageSize <= 0 || limits.PageSize > 1000 ||
		limits.MaxVisited <= 0 || limits.MaxVisited > 100_000 ||
		limits.MaxDocuments <= 0 || limits.MaxDocuments > 50_000 ||
		limits.MaxFileBytes <= 0 || limits.MaxFileBytes > 64<<20 {
		return nil, ErrLimit
	}
	if resolver == nil {
		resolver = NewRefResolver(nil)
	}
	return &Adapter{
		limits:    limits,
		resolver:  resolver,
		repoStore: repoStore,
		walker:    NewTreeWalker(limits),
	}, nil
}

// NewDefaultAdapter constructs an Adapter with default bounds.
func NewDefaultAdapter(repoStore RepositoryOpener) *Adapter {
	adapter, err := NewAdapter(DefaultLimits(), nil, repoStore)
	if err != nil {
		panic(err)
	}
	return adapter
}

// List returns one deterministic, bounded page of descriptors pinned to an immutable snapshot.
func (a *Adapter) List(ctx context.Context, source knowl.Source, pageToken string) (knowl.DocumentPage, error) {
	if err := ctx.Err(); err != nil {
		return knowl.DocumentPage{}, err
	}
	if source.Type != knowl.SourceTypeGit || source.Config.Git == nil {
		return knowl.DocumentPage{}, app.ErrSourceInvalid
	}
	gitCfg := *source.Config.Git

	cursor, err := decodePageToken(pageToken)
	if err != nil {
		return knowl.DocumentPage{}, err
	}

	var snapshotHash plumbing.Hash
	if cursor == nil {
		prepared, err := a.PrepareSnapshot(ctx, source, "")
		if err != nil {
			return knowl.DocumentPage{}, err
		}
		cursor, err = decodePageToken(prepared.PageToken)
		if err != nil {
			return knowl.DocumentPage{}, err
		}
		snapshotHash = plumbing.NewHash(cursor.Snapshot)
	} else {
		snapshotHash = plumbing.NewHash(cursor.Snapshot)
		if snapshotHash.IsZero() {
			return knowl.DocumentPage{}, WrapClassified(ClassScanInvalid, ErrPageToken, "invalid snapshot in page token")
		}
	}

	if a.repoStore == nil {
		return knowl.DocumentPage{}, WrapClassified(ClassScanInvalid, ErrScanInvalid, "repository store not configured")
	}

	repo, err := a.repoStore.OpenOrClone(ctx, source)
	if err != nil {
		return knowl.DocumentPage{}, err
	}

	matchers, err := CompileMatchers(gitCfg.Include)
	if err != nil {
		return knowl.DocumentPage{}, WrapClassified(ClassScanInvalid, err, "invalid include patterns")
	}

	allDocs, err := a.walker.Walk(ctx, repo.Storer, snapshotHash, matchers)
	if err != nil {
		return knowl.DocumentPage{}, err
	}

	startIndex := 0
	if cursor != nil && cursor.After != "" {
		startIndex = len(allDocs)
		for i, doc := range allDocs {
			if doc.Path > cursor.After {
				startIndex = i
				break
			}
		}
	}

	endIndex := min(startIndex+a.limits.PageSize, len(allDocs))
	pagedDocs := allDocs[startIndex:endIndex]

	page := knowl.DocumentPage{
		Documents: pagedDocs,
	}

	if endIndex < len(allDocs) && len(pagedDocs) > 0 {
		nextCursor := pageCursor{
			Version:  pageTokenVersion,
			Snapshot: snapshotHash.String(),
			After:    pagedDocs[len(pagedDocs)-1].Path,
		}
		nextToken, err := encodePageToken(nextCursor)
		if err != nil {
			return knowl.DocumentPage{}, err
		}
		page.NextPageToken = nextToken
	}

	if err := app.ValidateDocumentPage(page, a.limits.PageSize); err != nil {
		return knowl.DocumentPage{}, err
	}

	return page, nil
}

// PrepareSnapshot resolves the tracked ref once, refreshes the disposable
// cache, validates history against the durable checkpoint, and returns the
// opaque first-page token that pins the whole scan.
func (a *Adapter) PrepareSnapshot(ctx context.Context, source knowl.Source, previousCheckpoint string) (app.SnapshotPreparation, error) {
	if err := ctx.Err(); err != nil {
		return app.SnapshotPreparation{}, err
	}
	if source.Type != knowl.SourceTypeGit || source.Config.Git == nil || a.repoStore == nil {
		return app.SnapshotPreparation{}, app.ErrSourceInvalid
	}
	config := *source.Config.Git
	resolved, err := a.resolver.ResolveRemoteRef(ctx, config)
	if err != nil {
		return app.SnapshotPreparation{}, err
	}
	repo, err := a.repoStore.OpenOrClone(ctx, source)
	if err != nil {
		return app.SnapshotPreparation{}, err
	}
	if err := a.resolver.ValidateHistory(repo.Storer, config, previousCheckpoint, resolved.Hash); err != nil {
		return app.SnapshotPreparation{}, err
	}
	token, err := encodePageToken(pageCursor{Version: pageTokenVersion, Snapshot: resolved.Hash.String()})
	if err != nil {
		return app.SnapshotPreparation{}, WrapClassified(ClassScanInvalid, err, "encode immutable snapshot token")
	}
	return app.SnapshotPreparation{PageToken: token, Checkpoint: resolved.Hash.String()}, nil
}

// Fetch returns one immutable source document blob from the snapshot.
func (a *Adapter) Fetch(ctx context.Context, source knowl.Source, ref knowl.DocumentRef) (knowl.Document, error) {
	if err := ctx.Err(); err != nil {
		return knowl.Document{}, err
	}
	if a.repoStore == nil {
		return knowl.Document{}, WrapClassified(ClassScanInvalid, ErrScanInvalid, "repository store not configured")
	}
	repo, err := a.repoStore.OpenOrClone(ctx, source)
	if err != nil {
		return knowl.Document{}, err
	}
	blobHash := plumbing.NewHash(ref.Revision)
	if blobHash.IsZero() {
		return knowl.Document{}, WrapClassified(ClassFetch, ErrDocumentNotFound, fmt.Sprintf("invalid blob revision %q", ref.Revision))
	}
	snapshotHash := plumbing.NewHash(ref.Metadata["snapshot"])
	if snapshotHash.IsZero() {
		return knowl.Document{}, WrapClassified(ClassScanInvalid, ErrScanInvalid, "document snapshot is missing or invalid")
	}
	commit, err := repo.CommitObject(snapshotHash)
	if err != nil {
		return knowl.Document{}, WrapClassified(ClassFetch, ErrDocumentNotFound, fmt.Sprintf("snapshot %s not found", snapshotHash))
	}
	file, err := commit.File(ref.Path)
	if err != nil {
		return knowl.Document{}, WrapClassified(ClassFetch, ErrDocumentNotFound, fmt.Sprintf("path %s not found in snapshot %s", ref.Path, snapshotHash))
	}
	if file.Hash != blobHash {
		return knowl.Document{}, WrapClassified(ClassFetch, ErrRevisionChanged, fmt.Sprintf("path %s revision does not match snapshot %s", ref.Path, snapshotHash))
	}
	blob, err := repo.BlobObject(blobHash)
	if err != nil {
		return knowl.Document{}, WrapClassified(ClassFetch, ErrDocumentNotFound, fmt.Sprintf("blob %s not found", ref.Revision))
	}
	if blob.Size > a.limits.MaxFileBytes {
		return knowl.Document{}, WrapClassified(ClassResourceLimit, ErrLimit, fmt.Sprintf("blob %s exceeds limit of %d bytes", ref.Revision, a.limits.MaxFileBytes))
	}
	reader, err := blob.Reader()
	if err != nil {
		return knowl.Document{}, WrapClassified(ClassFetch, err, fmt.Sprintf("cannot read blob %s", ref.Revision))
	}
	defer func() {
		_ = reader.Close()
	}()
	content, err := io.ReadAll(reader)
	if err != nil {
		return knowl.Document{}, WrapClassified(ClassFetch, err, fmt.Sprintf("failed reading blob %s", ref.Revision))
	}

	title := DocumentTitle(ref.Path, content)
	uriBase := ""
	if source.Config.Git != nil {
		uriBase = source.Config.Git.URIBase
	}
	uri := DocumentURI(source.ID, uriBase, ref.Metadata["snapshot"], ref.Path)
	mediaType := DocumentMediaType(ref.Path)

	doc := knowl.Document{
		DocumentRef: ref,
		Title:       title,
		URI:         uri,
		MediaType:   mediaType,
		Content:     content,
	}

	if err := app.ValidateDocument(doc, int(a.limits.MaxFileBytes)); err != nil {
		return knowl.Document{}, WrapClassified(ClassScanInvalid, err, "invalid document shape")
	}

	return doc, nil
}

// DocumentURI returns the document URI for a Git source:
// If uriBase is configured: uriBase joined with relPath
// Otherwise: knowl://sources/{sourceID}/{relPath}
func DocumentURI(sourceID knowl.SourceID, uriBase, snapshot, relPath string) string {
	relPath = strings.TrimPrefix(filepath.ToSlash(relPath), "/")
	if uriBase != "" {
		base, err := url.Parse(uriBase)
		if err == nil && base.IsAbs() {
			if snapshot != "" {
				base.Path = path.Join(base.Path, snapshot, relPath)
			} else {
				base.Path = path.Join(base.Path, relPath)
			}
			base.RawPath = ""
			return base.String()
		}
	}
	return fmt.Sprintf("knowl://sources/%s/%s", sourceID, relPath)
}

// DocumentMediaType reports the deterministic media type of a Git document path.
func DocumentMediaType(relative string) string {
	if strings.EqualFold(filepath.Ext(relative), ".md") {
		return "text/markdown"
	}
	if mediaType := mime.TypeByExtension(strings.ToLower(filepath.Ext(relative))); mediaType != "" {
		return mediaType
	}
	return "application/octet-stream"
}

// DocumentTitle derives the title from Markdown frontmatter/heading or file basename.
func DocumentTitle(relative string, content []byte) string {
	if strings.EqualFold(filepath.Ext(relative), ".md") {
		body, metadata := splitFrontmatter(string(content))
		if title, ok := metadata["title"].(string); ok && strings.TrimSpace(title) != "" {
			return strings.TrimSpace(title)
		}
		for _, line := range strings.Split(body, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "# ") && strings.TrimSpace(strings.TrimPrefix(trimmed, "# ")) != "" {
				return strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			}
		}
	}
	title := strings.TrimSuffix(filepath.Base(relative), filepath.Ext(relative))
	if strings.TrimSpace(title) == "" {
		return "Source document"
	}
	return title
}

func splitFrontmatter(content string) (string, map[string]any) {
	lines := strings.Split(content, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return content, nil
	}
	end := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			end = index
			break
		}
	}
	if end < 0 {
		return content, nil
	}
	metadata := make(map[string]any)
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &metadata); err != nil {
		return content, nil
	}
	return strings.Join(lines[end+1:], "\n"), metadata
}
