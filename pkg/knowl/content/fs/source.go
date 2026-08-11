package fs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/baldaworks/knowl/pkg/knowl/types"
	"gopkg.in/yaml.v3"
)

// AcceptSource persists one immutable source version and its manifest.
func (workspace *Workspace) AcceptSource(ctx context.Context, envelope knowl.SourceEnvelope) (knowl.AcceptedSource, error) {
	if err := contextErr(ctx); err != nil {
		return knowl.AcceptedSource{}, err
	}
	if strings.TrimSpace(string(envelope.Scope)) == "" || strings.TrimSpace(envelope.Source.Adapter) == "" || strings.TrimSpace(envelope.Source.ID) == "" || strings.TrimSpace(envelope.Version.Version) == "" || len(envelope.Content) == 0 || len(envelope.Content) > workspace.maxSourceBytes {
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
		if existing.Digest != digest {
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
		Scope:      string(envelope.Scope),
		Adapter:    envelope.Source.Adapter,
		ID:         envelope.Source.ID,
		Version:    envelope.Version.Version,
		Digest:     digest,
		MediaType:  envelope.MediaType,
		ReceivedAt: envelope.ReceivedAt,
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
