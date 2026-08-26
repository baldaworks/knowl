package fs

import (
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowl "github.com/baldaworks/knowl/pkg/knowl/types"
	"gopkg.in/yaml.v3"
)

// SourceDigests inventories regular canonical files beneath exactly one source
// namespace with their SHA-256 content digests, sorted by canonical path.
func (workspace *Workspace) SourceDigests(ctx context.Context, scope knowl.ScopeRef, sourceID knowl.SourceID, limit int) ([]app.SourceDigestEntry, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(scope)) == "" || app.ValidateSourceID(sourceID) != nil || app.ValidateSourceDigestLimit(limit) != nil {
		return nil, app.ErrSourceInvalid
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	prefix := "wiki/sources/" + string(sourceID) + "/"
	namespace := filepath.Join(workspace.root, filepath.FromSlash("wiki/sources/"+string(sourceID)))
	if _, err := os.Lstat(namespace); errors.Is(err, os.ErrNotExist) {
		return []app.SourceDigestEntry{}, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat source namespace: %w", err)
	}
	if err := rejectSymlinkPath(workspace.root, namespace); err != nil {
		return nil, err
	}
	entries := make([]app.SourceDigestEntry, 0, 16)
	walkErr := filepath.WalkDir(namespace, func(path string, item iofs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := contextErr(ctx); err != nil {
			return err
		}
		switch {
		case item.IsDir():
			return nil
		case !item.Type().IsRegular():
			return fmt.Errorf("source namespace entry is not a regular file: %w", ErrPathRejected)
		}
		relative, relErr := filepath.Rel(workspace.root, path)
		if relErr != nil {
			return relErr
		}
		canonical := filepath.ToSlash(relative)
		if !strings.HasPrefix(canonical, prefix) {
			return fmt.Errorf("source inventory path %q: %w", canonical, ErrPathRejected)
		}
		info, statErr := item.Info()
		if statErr != nil {
			return statErr
		}
		if info.Size() > maxSourceStageFile {
			return fmt.Errorf("source file %q exceeds the inventory bound: %w", canonical, ErrInvalidSource)
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read source file %q: %w", canonical, readErr)
		}
		if len(entries) >= limit {
			return app.ErrSourceMutationLimit
		}
		entries = append(entries, app.SourceDigestEntry{Path: canonical, Digest: digestBytes(content)})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Path < entries[right].Path })
	return entries, nil
}

// AcceptSource persists one immutable source version and its manifest.
func (workspace *Workspace) AcceptSource(ctx context.Context, envelope knowl.SourceEnvelope) (knowl.AcceptedSource, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.AcceptedSource{}, err
	}
	if strings.TrimSpace(string(envelope.Scope)) == "" || strings.TrimSpace(envelope.Source.Adapter) == "" || strings.TrimSpace(envelope.Source.ID) == "" || strings.TrimSpace(envelope.Version.Version) == "" || len(envelope.Content) > workspace.maxSourceBytes {
		return knowl.AcceptedSource{}, ErrInvalidSource
	}
	if envelope.SourceDocument != (knowl.SourceDocument{}) &&
		(app.ValidateSourceDocument(envelope.SourceDocument) != nil || envelope.SourceDocument.Revision != envelope.Version.Version) {
		return knowl.AcceptedSource{}, ErrInvalidSource
	}
	digest := digestBytes(envelope.Content)
	if strings.ToLower(strings.TrimSpace(envelope.Version.Digest)) != digest {
		return knowl.AcceptedSource{}, ErrDigestMismatch
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()

	keyDir := filepath.Join(workspace.root, workspaceRawDir, token(string(envelope.Scope)+"\x00"+envelope.Source.Adapter+"\x00"+envelope.Source.ID), token(envelope.Version.Version))
	sourcePath := filepath.Join(keyDir, "source")
	manifestPath := filepath.Join(keyDir, "manifest.yaml")
	if existing, err := readManifest(manifestPath); err == nil {
		if existing.Digest != digest ||
			(existing.SourceDocument != (knowl.SourceDocument{}) && existing.SourceDocument != envelope.SourceDocument) {
			return knowl.AcceptedSource{}, ErrSourceConflict
		}
		return existing.accepted(), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return knowl.AcceptedSource{}, fmt.Errorf("read source manifest: %w", err)
	}
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return knowl.AcceptedSource{}, fmt.Errorf("create source directory: %w", err)
	}
	manifest := sourceManifest{
		Scope:          string(envelope.Scope),
		Adapter:        envelope.Source.Adapter,
		ID:             envelope.Source.ID,
		Version:        envelope.Version.Version,
		Digest:         digest,
		MediaType:      envelope.MediaType,
		SourceDocument: envelope.SourceDocument,
		ReceivedAt:     envelope.ReceivedAt,
	}
	if manifest.ReceivedAt.IsZero() {
		manifest.ReceivedAt = time.Now().UTC()
	}
	manifestBytes, err := yaml.Marshal(manifest)
	if err != nil {
		return knowl.AcceptedSource{}, fmt.Errorf("marshal source manifest: %w", err)
	}
	if err := writeAtomic(sourcePath, envelope.Content, 0o600); err != nil {
		return knowl.AcceptedSource{}, fmt.Errorf("write source content: %w", err)
	}
	if err := writeAtomic(manifestPath, manifestBytes, 0o600); err != nil {
		_ = os.Remove(sourcePath)
		return knowl.AcceptedSource{}, fmt.Errorf("write source manifest: %w", err)
	}
	return manifest.accepted(), nil
}

// ReadSource returns the immutable source bytes previously accepted by the workspace.
func (workspace *Workspace) ReadSource(ctx context.Context, source knowl.AcceptedSource, limits knowl.ReadLimits) ([]byte, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(source.Scope)) == "" || strings.TrimSpace(source.Source.Adapter) == "" || strings.TrimSpace(source.Source.ID) == "" || strings.TrimSpace(source.Version.Version) == "" {
		return nil, fmt.Errorf("source identity is incomplete: %w", ErrInvalidSource)
	}

	maxBytes := limits.Bytes
	if maxBytes <= 0 || maxBytes > workspace.maxSourceBytes {
		maxBytes = workspace.maxSourceBytes
	}
	path := filepath.Join(workspace.root, workspaceRawDir, token(string(source.Scope)+"\x00"+source.Source.Adapter+"\x00"+source.Source.ID), token(source.Version.Version), "source")
	if err := rejectSymlinkPath(workspace.root, path); err != nil {
		return nil, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", source.Source.ID, ErrSourceNotFound)
		}
		return nil, fmt.Errorf("read source: %w", err)
	}
	if len(content) > maxBytes {
		return nil, fmt.Errorf("source exceeds %d bytes: %w", maxBytes, ErrInvalidSource)
	}
	if limits.Characters > 0 && utf8.RuneCount(content) > limits.Characters {
		return nil, fmt.Errorf("source exceeds %d characters: %w", limits.Characters, ErrInvalidSource)
	}
	if source.Version.Digest != "" && digestBytes(content) != strings.ToLower(source.Version.Digest) {
		return nil, ErrDigestMismatch
	}
	return content, nil
}
