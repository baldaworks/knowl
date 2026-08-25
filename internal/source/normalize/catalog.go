// Package normalize renders filesystem wiki documents into canonical source mirrors.
package normalize

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
)

const (
	// FormatVersion identifies the deterministic filesystem-wiki rendering contract.
	FormatVersion = "filesystem-wiki-v1"
	maxDigestText = 4096
)

var (
	// ErrInvalid reports malformed catalog or render identity input.
	ErrInvalid = errors.New("invalid source normalization input")
	// ErrLimit reports bounded normalizer input or output overflow.
	ErrLimit = errors.New("source normalization exceeds a limit")
)

// Limits bounds catalog construction and rendered values.
type Limits struct {
	MaxDocuments     int
	MaxPathBytes     int
	MaxMetadataBytes int
	MaxRenderedBytes int
}

// DefaultLimits returns conservative source normalizer bounds.
func DefaultLimits() Limits {
	return Limits{
		MaxDocuments:     50_000,
		MaxPathBytes:     2048,
		MaxMetadataBytes: 16 << 20,
		MaxRenderedBytes: 64 << 20,
	}
}

// EntryKind classifies one catalog entry.
type EntryKind string

const (
	// EntryMarkdown identifies a Markdown page.
	EntryMarkdown EntryKind = "markdown"
	// EntryAsset identifies an auxiliary asset.
	EntryAsset EntryKind = "asset"
)

// Entry is one canonical source-local catalog member.
type Entry struct {
	DocumentID knowl.DocumentID `json:"document_id"`
	Path       string           `json:"path"`
	Kind       EntryKind        `json:"kind"`
}

type resolution struct {
	Target    string `json:"target,omitempty"`
	Ambiguous bool   `json:"ambiguous,omitempty"`
}

type namedResolution struct {
	Key       string `json:"key"`
	Target    string `json:"target,omitempty"`
	Ambiguous bool   `json:"ambiguous,omitempty"`
}

// Catalog is an immutable deterministic resolution index for one source scan.
type Catalog struct {
	entries       []Entry
	digest        string
	noteExact     map[string]resolution
	noteBasename  map[string]resolution
	assetExact    map[string]resolution
	assetBasename map[string]resolution
}

// BuildCatalog validates, sorts, and indexes one complete descriptor set.
func BuildCatalog(refs []knowl.DocumentRef, limits Limits) (Catalog, error) {
	if !validLimits(limits) {
		return Catalog{}, ErrLimit
	}
	if len(refs) > limits.MaxDocuments {
		return Catalog{}, ErrLimit
	}
	entries := make([]Entry, 0, len(refs))
	seen := make(map[knowl.DocumentID]struct{}, len(refs))
	metadataBytes := 0
	for _, ref := range refs {
		if app.ValidateDocumentRef(ref) != nil || ref.ExternalID != knowl.DocumentID(ref.Path) || len(ref.Path) > limits.MaxPathBytes {
			return Catalog{}, ErrInvalid
		}
		if _, exists := seen[ref.ExternalID]; exists {
			return Catalog{}, ErrInvalid
		}
		seen[ref.ExternalID] = struct{}{}
		metadataBytes += descriptorSize(ref)
		if metadataBytes > limits.MaxMetadataBytes {
			return Catalog{}, ErrLimit
		}
		kind := EntryAsset
		if strings.EqualFold(path.Ext(ref.Path), ".md") {
			kind = EntryMarkdown
		}
		entries = append(entries, Entry{DocumentID: ref.ExternalID, Path: ref.Path, Kind: kind})
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Path < entries[right].Path
	})
	catalog := Catalog{
		entries:       entries,
		noteExact:     make(map[string]resolution),
		noteBasename:  make(map[string]resolution),
		assetExact:    make(map[string]resolution),
		assetBasename: make(map[string]resolution),
	}
	for _, entry := range entries {
		catalog.record(entry)
	}
	digest, err := catalogDigest(catalog)
	if err != nil {
		return Catalog{}, err
	}
	catalog.digest = digest
	return catalog, nil
}

// Digest returns the canonical SHA-256 catalog identity.
func (catalog Catalog) Digest() string {
	return catalog.digest
}

// Entries returns a detached sorted catalog snapshot.
func (catalog Catalog) Entries() []Entry {
	return append([]Entry(nil), catalog.entries...)
}

func (catalog Catalog) contains(documentID knowl.DocumentID, documentPath string) bool {
	index := sort.Search(len(catalog.entries), func(index int) bool {
		return catalog.entries[index].Path >= documentPath
	})
	return index < len(catalog.entries) && catalog.entries[index].Path == documentPath && catalog.entries[index].DocumentID == documentID
}

// ResolveNote resolves an exact or unique-basename Markdown reference to a
// source-local page ID without its Markdown extension.
func (catalog Catalog) ResolveNote(raw string) (string, bool) {
	key, ok := referenceKey(raw, true)
	if !ok {
		return "", false
	}
	return resolve(catalog.noteExact, catalog.noteBasename, key, path.Base(key))
}

// ResolveAsset resolves an exact or unique-basename asset reference to its
// source-relative target path.
func (catalog Catalog) ResolveAsset(raw string) (string, bool) {
	key, ok := referenceKey(raw, false)
	if !ok {
		return "", false
	}
	return resolve(catalog.assetExact, catalog.assetBasename, key, path.Base(key))
}

func (catalog *Catalog) record(entry Entry) {
	if entry.Kind == EntryMarkdown {
		target := trimMarkdownExtension(entry.Path)
		recordResolution(catalog.noteExact, target, target)
		recordResolution(catalog.noteBasename, path.Base(target), target)
		return
	}
	recordResolution(catalog.assetExact, entry.Path, entry.Path)
	recordResolution(catalog.assetBasename, path.Base(entry.Path), entry.Path)
}

func recordResolution(index map[string]resolution, key, target string) {
	current, exists := index[key]
	if !exists {
		index[key] = resolution{Target: target}
		return
	}
	if current.Target != target {
		index[key] = resolution{Ambiguous: true}
	}
}

func resolve(exact, basename map[string]resolution, exactKey, basenameKey string) (string, bool) {
	if result, exists := exact[exactKey]; exists && !result.Ambiguous && result.Target != "" {
		return result.Target, true
	}
	if result, exists := basename[basenameKey]; exists && !result.Ambiguous && result.Target != "" {
		return result.Target, true
	}
	return "", false
}

func referenceKey(raw string, markdown bool) (string, bool) {
	key := strings.TrimSpace(raw)
	key = strings.TrimPrefix(key, "./")
	if !validCanonicalPath(key) {
		return "", false
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
	}
	if markdown {
		key = trimMarkdownExtension(key)
	}
	return key, true
}

func trimMarkdownExtension(value string) string {
	extension := path.Ext(value)
	if strings.EqualFold(extension, ".md") {
		return value[:len(value)-len(extension)]
	}
	return value
}

func catalogDigest(catalog Catalog) (string, error) {
	encoded, err := json.Marshal(struct {
		FormatVersion string            `json:"format_version"`
		Entries       []Entry           `json:"entries"`
		NoteExact     []namedResolution `json:"note_exact"`
		NoteBasename  []namedResolution `json:"note_basename"`
		AssetExact    []namedResolution `json:"asset_exact"`
		AssetBasename []namedResolution `json:"asset_basename"`
	}{
		FormatVersion: FormatVersion,
		Entries:       catalog.entries,
		NoteExact:     sortedResolutions(catalog.noteExact),
		NoteBasename:  sortedResolutions(catalog.noteBasename),
		AssetExact:    sortedResolutions(catalog.assetExact),
		AssetBasename: sortedResolutions(catalog.assetBasename),
	})
	if err != nil {
		return "", ErrInvalid
	}
	return digestBytes(encoded), nil
}

func sortedResolutions(index map[string]resolution) []namedResolution {
	keys := make([]string, 0, len(index))
	for key := range index {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]namedResolution, 0, len(keys))
	for _, key := range keys {
		value := index[key]
		result = append(result, namedResolution{Key: key, Target: value.Target, Ambiguous: value.Ambiguous})
	}
	return result
}

// RenderedFile is one immutable deterministic normalizer output.
type RenderedFile struct {
	path    string
	content []byte
	digest  string
}

// NewRenderedFile validates and copies one rendered output.
func NewRenderedFile(filePath string, content []byte, limits Limits) (RenderedFile, error) {
	if !validLimits(limits) || len(filePath) > limits.MaxPathBytes || !validCanonicalPath(filePath) {
		return RenderedFile{}, ErrInvalid
	}
	if content == nil {
		return RenderedFile{}, ErrInvalid
	}
	if len(content) > limits.MaxRenderedBytes {
		return RenderedFile{}, ErrLimit
	}
	copyContent := append([]byte(nil), content...)
	return RenderedFile{path: filePath, content: copyContent, digest: digestBytes(copyContent)}, nil
}

// Path returns the canonical rendered target path.
func (file RenderedFile) Path() string {
	return file.path
}

// Content returns a detached copy of the rendered bytes.
func (file RenderedFile) Content() []byte {
	return append([]byte(nil), file.content...)
}

// Digest returns the rendered content SHA-256.
func (file RenderedFile) Digest() string {
	return file.digest
}

type mirrorFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// MirrorIdentity contains every independent input to a mirror digest.
type MirrorIdentity struct {
	SourceID      knowl.SourceID
	DocumentID    knowl.DocumentID
	Revision      string
	RawSource     knowl.AcceptedSource
	CatalogDigest string
	Files         []RenderedFile
}

// MirrorDigest returns the canonical digest for one rendered source document.
func MirrorDigest(identity MirrorIdentity) (string, error) {
	if app.ValidateSourceID(identity.SourceID) != nil || app.ValidateDocumentID(identity.DocumentID) != nil ||
		!validText(identity.Revision, maxDigestText) || !validSHA256(identity.CatalogDigest) || !validAcceptedSource(identity.RawSource) || len(identity.Files) == 0 {
		return "", ErrInvalid
	}
	files := make([]mirrorFile, 0, len(identity.Files))
	seen := make(map[string]struct{}, len(identity.Files))
	for _, file := range identity.Files {
		if !validCanonicalPath(file.path) || !validSHA256(file.digest) || digestBytes(file.content) != file.digest {
			return "", ErrInvalid
		}
		if _, exists := seen[file.path]; exists {
			return "", ErrInvalid
		}
		seen[file.path] = struct{}{}
		files = append(files, mirrorFile{Path: file.path, Digest: file.digest})
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	encoded, err := json.Marshal(struct {
		FormatVersion string               `json:"format_version"`
		SourceID      knowl.SourceID       `json:"source_id"`
		DocumentID    knowl.DocumentID     `json:"document_id"`
		Revision      string               `json:"revision"`
		RawSource     knowl.AcceptedSource `json:"raw_source"`
		CatalogDigest string               `json:"catalog_digest"`
		Files         []mirrorFile         `json:"files"`
	}{FormatVersion, identity.SourceID, identity.DocumentID, identity.Revision, identity.RawSource, identity.CatalogDigest, files})
	if err != nil {
		return "", ErrInvalid
	}
	return digestBytes(encoded), nil
}

func validAcceptedSource(source knowl.AcceptedSource) bool {
	return strings.TrimSpace(string(source.Scope)) != "" && validText(source.Source.Adapter, 255) && validText(source.Source.ID, 4096) &&
		validText(source.Version.Version, maxDigestText) && validSHA256(source.Version.Digest) && validText(source.MediaType, 255) && validText(source.ManifestRef, 4096)
}

func validLimits(limits Limits) bool {
	return limits.MaxDocuments > 0 && limits.MaxDocuments <= 100_000 && limits.MaxPathBytes > 0 && limits.MaxPathBytes <= 4096 &&
		limits.MaxMetadataBytes > 0 && limits.MaxMetadataBytes <= 64<<20 && limits.MaxRenderedBytes > 0 && limits.MaxRenderedBytes <= 64<<20
}

func validText(value string, maximum int) bool {
	if strings.TrimSpace(value) == "" || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < ' ' || character == 0x7f {
			return false
		}
	}
	return true
}

func validCanonicalPath(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || strings.Contains(value, "\\") || path.IsAbs(value) || path.Clean(value) != value {
		return false
	}
	for _, character := range value {
		if character < ' ' || character == 0x7f {
			return false
		}
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func digestBytes(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func descriptorSize(ref knowl.DocumentRef) int {
	size := len(ref.ExternalID) + len(ref.Revision) + len(ref.Path)
	for key, value := range ref.Metadata {
		size += len(key) + len(value)
	}
	return size
}
