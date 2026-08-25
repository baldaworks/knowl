package fs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/okf"
	knowlwiki "github.com/baldaworks/knowl/pkg/knowl/wiki"
	"gopkg.in/yaml.v3"
)

const (
	migrationOperationID   = "okf-v0.2"
	migrationMarkerPath    = ".knowl/migrations/okf-v0.2.yaml"
	migrationLegacyLogPath = ".knowl/migrations/okf-v0.2-legacy-log.md"
)

// MigrationResult describes one explicit canonical workspace migration.
type MigrationResult struct {
	Version string   `json:"version"`
	Changed bool     `json:"changed"`
	Files   []string `json:"files"`
}

type migrationMarker struct {
	Version     string    `yaml:"version"`
	Generation  string    `yaml:"generation"`
	CompletedAt time.Time `yaml:"completed_at"`
}

// MigrateOKFV02 explicitly converts a legacy workspace through the recoverable
// canonical writer. It is never called by validation, startup, or reads.
func (workspace *Workspace) MigrateOKFV02(ctx context.Context) (MigrationResult, error) {
	if err := contextErr(ctx); err != nil {
		return MigrationResult{}, err
	}
	if _, err := workspace.Recover(ctx); err != nil {
		return MigrationResult{}, fmt.Errorf("recover workspace before OKF migration: %w", err)
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()

	markerPath := filepath.Join(workspace.root, filepath.FromSlash(migrationMarkerPath))
	if marker, err := readMigrationMarker(markerPath); err == nil {
		return MigrationResult{Version: marker.Version, Files: []string{}}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return MigrationResult{}, fmt.Errorf("read OKF migration marker: %w", err)
	}

	entries, err := workspace.planOKFMigrationLocked()
	if err != nil {
		return MigrationResult{}, err
	}
	files := make([]string, len(entries))
	for index := range entries {
		files[index] = entries[index].target
	}
	generation := digestMigrationEntries(entries)
	if len(entries) > 0 {
		if _, err := workspace.commitLocked(canonicalCommitRequest{
			writer: stageWriterMigration, operationID: migrationOperationID, recoveryKey: migrationOperationID,
			generation: generation, files: files, entries: entries,
		}); err != nil {
			return MigrationResult{}, fmt.Errorf("commit OKF migration: %w", err)
		}
	}
	marker := migrationMarker{Version: okf.Version, Generation: generation, CompletedAt: workspace.now().UTC()}
	encoded, err := yaml.Marshal(marker)
	if err != nil {
		return MigrationResult{}, fmt.Errorf("encode OKF migration marker: %w", err)
	}
	if err := writeAtomic(markerPath, encoded, 0o600); err != nil {
		return MigrationResult{}, fmt.Errorf("write OKF migration marker: %w", err)
	}
	return MigrationResult{Version: okf.Version, Changed: len(entries) > 0, Files: files}, nil
}

func (workspace *Workspace) planOKFMigrationLocked() ([]canonicalCommitEntry, error) {
	wikiRoot := filepath.Join(workspace.root, workspaceWikiDir)
	if err := rejectSymlinkPath(workspace.root, wikiRoot); err != nil {
		return nil, err
	}
	var paths []string
	err := filepath.WalkDir(wikiRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink migration path %q: %w", path, ErrPathRejected)
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != markdownExt {
			return nil
		}
		relative, err := filepath.Rel(workspace.root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		if len(paths) > maxRecoveryEntries-1 {
			return fmt.Errorf("migration file count exceeds limit: %w", ErrWorkspaceInvalid)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("inspect legacy workspace: %w", err)
	}
	sort.Strings(paths)
	limits := okfLimits(workspace.maxSourceBytes)
	entries := make([]canonicalCommitEntry, 0, len(paths)+1)
	for _, target := range paths {
		absolute := filepath.Join(workspace.root, filepath.FromSlash(target))
		content, err := os.ReadFile(absolute)
		if err != nil {
			return nil, fmt.Errorf("read migration target %q: %w", target, err)
		}
		if len(content) > limits.MaxBytes {
			return nil, fmt.Errorf("migration target %q exceeds byte limit: %w", target, ErrWorkspaceInvalid)
		}
		bundleRelative := strings.TrimPrefix(target, workspaceWikiDir+"/")
		migrated, err := migrateOKFDocument(bundleRelative, content, limits)
		if err != nil {
			return nil, fmt.Errorf("migrate %q: %w", target, err)
		}
		if migrated == nil {
			continue
		}
		if bundleRelative == okfLogFilename {
			archivePath := filepath.Join(workspace.root, filepath.FromSlash(migrationLegacyLogPath))
			if existing, readErr := os.ReadFile(archivePath); readErr == nil {
				if string(existing) != string(content) {
					return nil, fmt.Errorf("legacy log archive collision: %w", ErrPrecondition)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				return nil, fmt.Errorf("read legacy log archive: %w", readErr)
			} else {
				entries = append(entries, migrationWriteEntry(migrationLegacyLogPath, nil, content))
			}
		}
		entries = append(entries, migrationWriteEntry(target, content, migrated))
	}
	return entries, nil
}

func migrateOKFDocument(relative string, content []byte, limits okf.Limits) ([]byte, error) {
	kind, err := okf.ClassifyPath(relative)
	if err != nil {
		return nil, err
	}
	switch kind {
	case okf.DocumentIndex:
		index, err := okf.ValidateIndex(relative, content, limits)
		if err != nil {
			return nil, err
		}
		if relative != okfIndexFilename || index.ObservedVersion == okf.Version {
			return nil, nil
		}
		index.ObservedVersion = okf.Version
		return okf.RenderIndex(relative, index, limits)
	case okf.DocumentLog:
		if _, validationErr := okf.ValidateLog(relative, content, limits); validationErr == nil {
			return nil, nil
		} else if relative != okfLogFilename {
			return nil, validationErr
		}
		return okf.RenderLog(relative, okf.Log{Title: "Knowl Update Log"}, limits)
	case okf.DocumentConcept:
		document, err := okf.ParseConcept(relative, content, limits)
		if err != nil {
			return nil, err
		}
		metadata, changed, err := knowlwiki.MigrateLegacyEnvelope(document.Metadata)
		if err != nil {
			return nil, err
		}
		if !changed {
			return nil, nil
		}
		document.Metadata = metadata
		return okf.RenderConcept(relative, document, limits)
	default:
		return nil, nil
	}
}

func migrationWriteEntry(target string, old, content []byte) canonicalCommitEntry {
	expected := ""
	if old != nil {
		expected = digestBytes(old)
	}
	return canonicalCommitEntry{action: "write", target: target, expectedDigest: expected, digest: digestBytes(content), content: content}
}

func digestMigrationEntries(entries []canonicalCommitEntry) string {
	var builder strings.Builder
	for _, entry := range entries {
		builder.WriteString(entry.target)
		builder.WriteByte(0)
		builder.WriteString(entry.digest)
		builder.WriteByte('\n')
	}
	return digestBytes([]byte(builder.String()))
}

func readMigrationMarker(path string) (migrationMarker, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return migrationMarker{}, err
	}
	var marker migrationMarker
	if err := yaml.Unmarshal(content, &marker); err != nil || marker.Version != okf.Version || marker.CompletedAt.IsZero() || !validSHA256(marker.Generation) {
		return migrationMarker{}, ErrWorkspaceInvalid
	}
	return marker, nil
}
