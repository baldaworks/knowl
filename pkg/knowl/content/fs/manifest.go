package fs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/types"
	"gopkg.in/yaml.v3"
)

func validSourceManifest(manifest sourceManifest) bool {
	if strings.TrimSpace(manifest.Scope) == "" || strings.TrimSpace(manifest.Adapter) == "" || strings.TrimSpace(manifest.ID) == "" || strings.TrimSpace(manifest.Version) == "" || len(manifest.Digest) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(manifest.Digest)
	return err == nil
}

type sourceManifest struct {
	Scope      string    `yaml:"scope"`
	Adapter    string    `yaml:"adapter"`
	ID         string    `yaml:"id"`
	Version    string    `yaml:"version"`
	Digest     string    `yaml:"digest"`
	MediaType  string    `yaml:"media_type"`
	ReceivedAt time.Time `yaml:"received_at"`
}

func (manifest sourceManifest) accepted() knowl.AcceptedSource {
	return knowl.AcceptedSource{Scope: knowl.ScopeRef(manifest.Scope), Source: knowl.SourceRef{Adapter: manifest.Adapter, ID: manifest.ID}, Version: knowl.SourceVersion{Version: manifest.Version, Digest: manifest.Digest}, MediaType: manifest.MediaType, ManifestRef: filepath.ToSlash(filepath.Join(workspaceRawDir, token(manifest.Scope+"\x00"+manifest.Adapter+"\x00"+manifest.ID), token(manifest.Version), "manifest.yaml"))}
}

type stageEntry struct {
	Action         knowl.SourceMutationAction `yaml:"action,omitempty"`
	Target         string                     `yaml:"target"`
	ExpectedDigest string                     `yaml:"expected_digest,omitempty"`
	Digest         string                     `yaml:"digest"`
}

type stageManifest struct {
	OperationID       string       `yaml:"operation_id"`
	Writer            string       `yaml:"writer,omitempty"`
	SourceID          string       `yaml:"source_id,omitempty"`
	Scope             string       `yaml:"scope,omitempty"`
	SchemaDigest      string       `yaml:"schema_digest"`
	SourceRefs        []string     `yaml:"source_refs,omitempty"`
	Entries           []stageEntry `yaml:"entries"`
	LogExpectedDigest string       `yaml:"log_expected_digest,omitempty"`
	LogDigest         string       `yaml:"log_digest,omitempty"`
	LogDate           string       `yaml:"log_date,omitempty"`
}

const (
	stageWriterMaintainer = "maintainer"
	stageWriterSource     = "source"
	stageWriterMigration  = "migration"
)

func manifestWriter(manifest stageManifest) string {
	if manifest.Writer == "" {
		return stageWriterMaintainer
	}
	return manifest.Writer
}

func entryAction(entry stageEntry) knowl.SourceMutationAction {
	if entry.Action == "" {
		return knowl.SourceMutationWrite
	}
	return entry.Action
}

type recoveryEntry struct {
	Action knowl.SourceMutationAction `yaml:"action,omitempty"`
	Target string                     `yaml:"target"`
	Backup string                     `yaml:"backup,omitempty"`
	HadOld bool                       `yaml:"had_old"`
	Mode   uint32                     `yaml:"mode,omitempty"`
	Digest string                     `yaml:"digest,omitempty"`
}

type recoveryJournal struct {
	OperationID string          `yaml:"operation_id"`
	Writer      string          `yaml:"writer,omitempty"`
	SourceID    string          `yaml:"source_id,omitempty"`
	Scope       string          `yaml:"scope,omitempty"`
	State       string          `yaml:"state"`
	Entries     []recoveryEntry `yaml:"entries"`
	Generation  string          `yaml:"generation,omitempty"`
	Files       []string        `yaml:"files,omitempty"`
}

type commitReceipt struct {
	Writer      string   `yaml:"writer"`
	SourceID    string   `yaml:"source_id"`
	Scope       string   `yaml:"scope"`
	OperationID string   `yaml:"operation_id"`
	Generation  string   `yaml:"generation"`
	Files       []string `yaml:"files"`
}

type logEntry struct {
	OperationID  string   `json:"operation_id"`
	Generation   string   `json:"generation"`
	SchemaDigest string   `json:"schema_digest"`
	SourceRefs   []string `json:"source_refs,omitempty"`
	Files        []string `json:"files"`
}

func readManifest(path string) (sourceManifest, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return sourceManifest{}, err
	}
	var manifest sourceManifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		return sourceManifest{}, err
	}
	return manifest, nil
}

func readStageManifest(path string) (stageManifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return stageManifest{}, err
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, maxStageManifestBytes+1))
	if err != nil {
		return stageManifest{}, err
	}
	if len(content) > maxStageManifestBytes {
		return stageManifest{}, ErrPlanConflict
	}
	var manifest stageManifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		return stageManifest{}, err
	}
	return manifest, nil
}

func writeJournal(path string, journal recoveryJournal) error {
	content, err := yaml.Marshal(journal)
	if err != nil {
		return fmt.Errorf("marshal recovery journal: %w", err)
	}
	if len(content) > maxRecoveryJournalBytes {
		return fmt.Errorf("recovery journal exceeds limit: %w", ErrWorkspaceInvalid)
	}
	if err := writeAtomic(path, content, 0o600); err != nil {
		return fmt.Errorf("write recovery journal: %w", err)
	}
	return nil
}

func readJournal(path string) (recoveryJournal, error) {
	file, err := os.Open(path)
	if err != nil {
		return recoveryJournal{}, err
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, maxRecoveryJournalBytes+1))
	if err != nil || len(content) > maxRecoveryJournalBytes {
		return recoveryJournal{}, ErrWorkspaceInvalid
	}
	var journal recoveryJournal
	if err := yaml.Unmarshal(content, &journal); err != nil {
		return recoveryJournal{}, err
	}
	return journal, nil
}

func readCommitReceipt(path string) (commitReceipt, error) {
	file, err := os.Open(path)
	if err != nil {
		return commitReceipt{}, err
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, maxStageManifestBytes+1))
	if err != nil || len(content) > maxStageManifestBytes {
		return commitReceipt{}, ErrPlanConflict
	}
	var receipt commitReceipt
	if err := yaml.Unmarshal(content, &receipt); err != nil {
		return commitReceipt{}, err
	}
	return receipt, nil
}
