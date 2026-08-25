// Package filesystem implements the built-in bounded filesystem source adapter.
package filesystem

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"github.com/gobwas/glob"
	"gopkg.in/yaml.v3"
)

const (
	tokenVersion = 1
	maxEntries   = 100_000
)

var (
	// ErrLimit reports that a configured filesystem source exceeded a bound.
	ErrLimit = errors.New("filesystem source exceeds a limit")
	// ErrPathRejected reports an unsafe or unsupported filesystem path.
	ErrPathRejected = errors.New("filesystem source path rejected")
	// ErrPageToken reports an invalid or stale filesystem page token.
	ErrPageToken = errors.New("filesystem source page token invalid")
	// ErrDocumentNotFound reports that a listed document is no longer present.
	ErrDocumentNotFound = errors.New("filesystem source document not found")
	// ErrRevisionChanged reports that a document changed after it was listed.
	ErrRevisionChanged = errors.New("filesystem source document revision changed")
)

// Limits bounds one adapter invocation. Values are validated by New.
type Limits struct {
	PageSize         int
	MaxVisited       int
	MaxDocuments     int
	MaxPathBytes     int
	MaxTokenBytes    int
	MaxFileBytes     int64
	MaxMetadataBytes int
}

// DefaultLimits returns conservative local filesystem adapter bounds.
func DefaultLimits() Limits {
	return Limits{
		PageSize:         256,
		MaxVisited:       100_000,
		MaxDocuments:     50_000,
		MaxPathBytes:     1024,
		MaxTokenBytes:    4096,
		MaxFileBytes:     64 << 20,
		MaxMetadataBytes: 16 << 20,
	}
}

// Adapter lists filesystem descriptors without retaining their content.
type Adapter struct {
	limits Limits
}

// New constructs an Adapter with explicit bounded limits.
func New(limits Limits) (*Adapter, error) {
	if limits.PageSize <= 0 || limits.PageSize > 1000 ||
		limits.MaxVisited <= 0 || limits.MaxVisited > maxEntries || limits.MaxDocuments <= 0 ||
		limits.MaxDocuments > maxEntries || limits.MaxDocuments > limits.MaxVisited ||
		limits.MaxPathBytes <= 0 || limits.MaxPathBytes > 4096 ||
		limits.MaxTokenBytes <= 0 || limits.MaxTokenBytes > 4096 ||
		limits.MaxFileBytes <= 0 || limits.MaxFileBytes > 64<<20 ||
		limits.MaxMetadataBytes <= 0 || limits.MaxMetadataBytes > 64<<20 {
		return nil, ErrLimit
	}
	return &Adapter{limits: limits}, nil
}

// NewDefault constructs an Adapter with conservative local bounds.
func NewDefault() *Adapter {
	adapter, err := New(DefaultLimits())
	if err != nil {
		panic(err)
	}
	return adapter
}

// List returns one deterministic page of content-free descriptors.
func (adapter *Adapter) List(ctx context.Context, source knowl.Source, pageToken string) (knowl.DocumentPage, error) {
	if err := contextError(ctx); err != nil {
		return knowl.DocumentPage{}, err
	}
	config, matchers, err := adapter.validateSource(source)
	if err != nil {
		return knowl.DocumentPage{}, err
	}
	cursor, err := adapter.decodeToken(pageToken)
	if err != nil {
		return knowl.DocumentPage{}, err
	}
	documents, err := adapter.scan(ctx, source.ID, *config, matchers)
	if err != nil {
		return knowl.DocumentPage{}, err
	}
	start, err := pageStart(documents, cursor)
	if err != nil {
		return knowl.DocumentPage{}, err
	}
	end := min(start+adapter.limits.PageSize, len(documents))
	page := knowl.DocumentPage{Documents: cloneRefs(documents[start:end])}
	if end < len(documents) {
		page.NextPageToken, err = adapter.encodeToken(documents[end-1].Path)
		if err != nil {
			return knowl.DocumentPage{}, err
		}
	}
	return page, nil
}

// Fetch selectively returns one exact listed source revision.
func (adapter *Adapter) Fetch(ctx context.Context, source knowl.Source, ref knowl.DocumentRef) (knowl.Document, error) {
	if err := contextError(ctx); err != nil {
		return knowl.Document{}, err
	}
	config, matchers, err := adapter.validateSource(source)
	if err != nil {
		return knowl.Document{}, err
	}
	if app.ValidateDocumentRef(ref) != nil || ref.ExternalID != knowl.DocumentID(ref.Path) || !matchesAny(matchers, ref.Path) {
		return knowl.Document{}, app.ErrSourceInvalid
	}
	target := filepath.Join(config.Root, filepath.FromSlash(ref.Path))
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return knowl.Document{}, fmt.Errorf("source %q document %q: %w", source.ID, ref.Path, ErrDocumentNotFound)
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return knowl.Document{}, fmt.Errorf("source %q document %q: %w", source.ID, ref.Path, ErrPathRejected)
	}
	if err := rejectSymlinkComponents(target); err != nil {
		return knowl.Document{}, fmt.Errorf("source %q document %q: %w", source.ID, ref.Path, ErrPathRejected)
	}
	if info.Size() > adapter.limits.MaxFileBytes {
		return knowl.Document{}, fmt.Errorf("source %q document %q: %w", source.ID, ref.Path, ErrLimit)
	}
	content, err := readFile(ctx, target, adapter.limits.MaxFileBytes)
	if err != nil {
		return knowl.Document{}, fmt.Errorf("source %q document %q: %w", source.ID, ref.Path, err)
	}
	if digestBytes(content) != ref.Revision {
		return knowl.Document{}, fmt.Errorf("source %q document %q: %w", source.ID, ref.Path, ErrRevisionChanged)
	}
	document := knowl.Document{
		DocumentRef: cloneRefs([]knowl.DocumentRef{ref})[0],
		Title:       documentTitle(ref.Path, content),
		URI:         documentURI(*config, target, ref.Path),
		MediaType:   documentMediaType(ref.Path),
		Content:     content,
	}
	if app.ValidateDocument(document, int(adapter.limits.MaxFileBytes)) != nil {
		return knowl.Document{}, app.ErrSourceInvalid
	}
	return document, nil
}

type pageCursor struct {
	Version int    `json:"version"`
	After   string `json:"after"`
}

func (adapter *Adapter) decodeToken(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) > adapter.limits.MaxTokenBytes {
		return "", ErrPageToken
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) > adapter.limits.MaxTokenBytes {
		return "", ErrPageToken
	}
	var cursor pageCursor
	if json.Unmarshal(decoded, &cursor) != nil || cursor.Version != tokenVersion || app.ValidateDocumentID(knowl.DocumentID(cursor.After)) != nil {
		return "", ErrPageToken
	}
	return cursor.After, nil
}

func (adapter *Adapter) encodeToken(after string) (string, error) {
	encoded, err := json.Marshal(pageCursor{Version: tokenVersion, After: after})
	if err != nil {
		return "", fmt.Errorf("encode filesystem page token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(encoded)
	if len(token) > adapter.limits.MaxTokenBytes {
		return "", ErrLimit
	}
	return token, nil
}

func (adapter *Adapter) validateSource(source knowl.Source) (*knowl.FilesystemSourceConfig, []glob.Glob, error) {
	if !source.Enabled || app.ValidateSource(source) != nil || source.Config.Filesystem == nil {
		return nil, nil, app.ErrSourceInvalid
	}
	config := source.Config.Filesystem
	if !filepath.IsAbs(config.Root) || filepath.Clean(config.Root) != config.Root || len(config.Include) == 0 ||
		(config.Flavor != knowl.SourceFlavorMarkdown && config.Flavor != knowl.SourceFlavorObsidian && config.Flavor != knowl.SourceFlavorOKF) {
		return nil, nil, app.ErrSourceInvalid
	}
	if !validURIBase(config.URIBase) {
		return nil, nil, app.ErrSourceInvalid
	}
	if err := rejectSymlinkComponents(config.Root); err != nil {
		return nil, nil, fmt.Errorf("source %q root: %w", source.ID, err)
	}
	info, err := os.Lstat(config.Root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("source %q root: %w", source.ID, ErrPathRejected)
	}
	matchers := make([]glob.Glob, 0, len(config.Include))
	for _, pattern := range config.Include {
		if !validIncludePattern(pattern) {
			return nil, nil, app.ErrSourceInvalid
		}
		compiled, compileErr := compilePattern(pattern)
		if compileErr != nil {
			return nil, nil, app.ErrSourceInvalid
		}
		matchers = append(matchers, compiled...)
	}
	return config, matchers, nil
}

func validIncludePattern(pattern string) bool {
	if pattern == "" || len(pattern) > 1024 || strings.TrimSpace(pattern) != pattern || strings.Contains(pattern, "\\") ||
		strings.HasPrefix(pattern, "/") || path.Clean(pattern) != pattern {
		return false
	}
	for _, part := range strings.Split(pattern, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func compilePattern(pattern string) ([]glob.Glob, error) {
	patterns := []string{pattern}
	for index := 0; index < len(patterns); index++ {
		current := patterns[index]
		position := strings.Index(current, "**/")
		if position < 0 {
			continue
		}
		withoutDirectory := current[:position] + current[position+3:]
		if !containsString(patterns, withoutDirectory) {
			patterns = append(patterns, withoutDirectory)
			if len(patterns) > 64 {
				return nil, ErrLimit
			}
		}
	}
	compiled := make([]glob.Glob, 0, len(patterns))
	for _, candidate := range patterns {
		matcher, err := glob.Compile(candidate, '/')
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, matcher)
	}
	return compiled, nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

type scanState struct {
	visited       int
	metadataBytes int
	documents     []knowl.DocumentRef
}

func (adapter *Adapter) scan(ctx context.Context, sourceID knowl.SourceID, config knowl.FilesystemSourceConfig, matchers []glob.Glob) ([]knowl.DocumentRef, error) {
	state := scanState{documents: make([]knowl.DocumentRef, 0)}
	err := filepath.WalkDir(config.Root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("source %q traversal failed: %w", sourceID, ErrPathRejected)
		}
		if err := contextError(ctx); err != nil {
			return err
		}
		state.visited++
		if state.visited > adapter.limits.MaxVisited {
			return ErrLimit
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("source %q contains a symlink: %w", sourceID, ErrPathRejected)
		}
		if path != config.Root && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := canonicalRelative(config.Root, path, adapter.limits.MaxPathBytes)
		if err != nil {
			return err
		}
		if !matchesAny(matchers, relative) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("source %q entry %q is not regular: %w", sourceID, relative, ErrPathRejected)
		}
		ref, err := adapter.describe(ctx, sourceID, path, relative, info.Size())
		if err != nil {
			return err
		}
		state.metadataBytes += descriptorSize(ref)
		if len(state.documents) >= adapter.limits.MaxDocuments || state.metadataBytes > adapter.limits.MaxMetadataBytes {
			return ErrLimit
		}
		state.documents = append(state.documents, ref)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(state.documents, func(left, right int) bool {
		return state.documents[left].Path < state.documents[right].Path
	})
	return state.documents, nil
}

func (adapter *Adapter) describe(ctx context.Context, sourceID knowl.SourceID, fullPath, relative string, size int64) (knowl.DocumentRef, error) {
	if size < 0 || size > adapter.limits.MaxFileBytes {
		return knowl.DocumentRef{}, fmt.Errorf("source %q document %q: %w", sourceID, relative, ErrLimit)
	}
	digest, err := hashFile(ctx, fullPath, adapter.limits.MaxFileBytes)
	if err != nil {
		return knowl.DocumentRef{}, fmt.Errorf("source %q document %q: %w", sourceID, relative, err)
	}
	kind := "asset"
	if strings.EqualFold(filepath.Ext(relative), ".md") {
		kind = "markdown"
	}
	metadata := map[string]string{"kind": kind}
	if mediaType := mime.TypeByExtension(strings.ToLower(filepath.Ext(relative))); mediaType != "" {
		metadata["media_type"] = mediaType
	}
	ref := knowl.DocumentRef{ExternalID: knowl.DocumentID(relative), Revision: digest, Path: relative, Metadata: metadata}
	if app.ValidateDocumentRef(ref) != nil {
		return knowl.DocumentRef{}, app.ErrSourceInvalid
	}
	return ref, nil
}

func hashFile(ctx context.Context, path string, maximum int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", ErrPathRejected
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", ErrPathRejected
	}
	if info.Size() > maximum {
		return "", ErrLimit
	}
	hash := sha256.New()
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if err := contextError(ctx); err != nil {
			return "", err
		}
		read, readErr := file.Read(buffer)
		total += int64(read)
		if total > maximum {
			return "", ErrLimit
		}
		if read > 0 {
			_, _ = hash.Write(buffer[:read])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", ErrPathRejected
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readFile(ctx context.Context, filePath string, maximum int64) ([]byte, error) {
	file, err := os.Open(filePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrDocumentNotFound
	}
	if err != nil {
		return nil, ErrPathRejected
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrPathRejected
	}
	if info.Size() > maximum {
		return nil, ErrLimit
	}
	content := make([]byte, 0, info.Size())
	buffer := make([]byte, 32<<10)
	for {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		read, readErr := file.Read(buffer)
		if int64(len(content)) > maximum-int64(read) {
			return nil, ErrLimit
		}
		content = append(content, buffer[:read]...)
		if errors.Is(readErr, io.EOF) {
			return content, nil
		}
		if readErr != nil {
			return nil, ErrPathRejected
		}
	}
}

func digestBytes(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func validURIBase(value string) bool {
	if value == "" {
		return true
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.IsAbs() && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") &&
		parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && strings.TrimRight(parsed.String(), "/") == value
}

func documentURI(config knowl.FilesystemSourceConfig, target, relative string) string {
	if config.URIBase != "" {
		base, _ := url.Parse(config.URIBase)
		base.Path = strings.TrimRight(base.Path, "/") + "/" + relative
		base.RawPath = ""
		return base.String()
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(target)}).String()
}

// DocumentURI reconstructs the canonical descriptor URI from configuration and
// the document's source-relative path without touching the source root.
func DocumentURI(config knowl.FilesystemSourceConfig, relative string) string {
	return documentURI(config, filepath.Join(config.Root, filepath.FromSlash(relative)), relative)
}

// DocumentMediaType reports the deterministic media type of one descriptor.
func DocumentMediaType(relative string) string {
	return documentMediaType(relative)
}

// DocumentTitle derives the deterministic fallback title of one descriptor.
func DocumentTitle(relative string, content []byte) string {
	return documentTitle(relative, content)
}

func documentMediaType(relative string) string {
	if strings.EqualFold(filepath.Ext(relative), ".md") {
		return "text/markdown"
	}
	if mediaType := mime.TypeByExtension(strings.ToLower(filepath.Ext(relative))); mediaType != "" {
		return mediaType
	}
	return "application/octet-stream"
}

func documentTitle(relative string, content []byte) string {
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

func canonicalRelative(root, target string, maximum int) (string, error) {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return "", ErrPathRejected
	}
	relative = filepath.ToSlash(relative)
	if relative == "" || relative == "." || len(relative) > maximum || !utf8.ValidString(relative) ||
		app.ValidateDocumentID(knowl.DocumentID(relative)) != nil {
		return "", ErrPathRejected
	}
	return relative, nil
}

func rejectSymlinkComponents(target string) error {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return ErrPathRejected
	}
	volume := filepath.VolumeName(absolute)
	current := volume + string(filepath.Separator)
	remaining := strings.TrimPrefix(absolute, current)
	for _, part := range strings.Split(remaining, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return ErrPathRejected
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrPathRejected
		}
	}
	return nil
}

func matchesAny(matchers []glob.Glob, relative string) bool {
	for _, matcher := range matchers {
		if matcher.Match(relative) {
			return true
		}
	}
	return false
}

func pageStart(documents []knowl.DocumentRef, cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	index := sort.Search(len(documents), func(index int) bool {
		return documents[index].Path >= cursor
	})
	if index == len(documents) || documents[index].Path != cursor {
		return 0, ErrPageToken
	}
	return index + 1, nil
}

func cloneRefs(input []knowl.DocumentRef) []knowl.DocumentRef {
	result := make([]knowl.DocumentRef, len(input))
	for index, ref := range input {
		result[index] = ref
		result[index].Metadata = make(map[string]string, len(ref.Metadata))
		for key, value := range ref.Metadata {
			result[index].Metadata[key] = value
		}
	}
	return result
}

func descriptorSize(ref knowl.DocumentRef) int {
	size := len(ref.ExternalID) + len(ref.Revision) + len(ref.Path)
	for key, value := range ref.Metadata {
		size += len(key) + len(value)
	}
	return size
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

var _ app.SourceAdapter = (*Adapter)(nil)
