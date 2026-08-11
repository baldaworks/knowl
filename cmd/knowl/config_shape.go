package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/baldaworks/knowl/pkg/knowl"
	knowltypes "github.com/baldaworks/knowl/pkg/knowl/types"
)

// AppConfig is the Knowl section of the Balda-compatible config document.
type AppConfig struct {
	Provider  string              `mapstructure:"provider"`
	Workspace WorkspaceConfig     `mapstructure:"workspace"`
	Storage   StorageConfig       `mapstructure:"storage"`
	Scope     knowltypes.ScopeRef `mapstructure:"scope"`
	Server    ServerConfig        `mapstructure:"server"`
	Ingest    IngestConfig        `mapstructure:"ingest"`
}

// WorkspaceConfig controls the workspace root used by Knowl.
type WorkspaceConfig struct {
	Path string `mapstructure:"path"`
}

// StorageConfig selects one operational storage backend and its typed options.
type StorageConfig struct {
	Type     string          `mapstructure:"type"`
	SQLite   *SQLiteConfig   `mapstructure:"sqlite"`
	Postgres *PostgresConfig `mapstructure:"postgres"`
}

// SQLiteConfig configures the SQLite operational store.
type SQLiteConfig struct {
	Path string `mapstructure:"path"`
}

// PostgresConfig configures the PostgreSQL operational store.
type PostgresConfig struct {
	DSN string `mapstructure:"dsn"`
}

// StorageSettings is the normalized backend selection consumed by host wiring.
type StorageSettings struct {
	Driver string
	Path   string
	DSN    string
}

// Normalize validates the storage discriminated union and resolves its
// workspace-relative values without opening the selected backend.
func (storage StorageConfig) Normalize(workspace string) (StorageSettings, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return StorageSettings{}, fmt.Errorf("knowl.workspace.path is required for storage normalization")
	}
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return StorageSettings{}, fmt.Errorf("resolve workspace for storage: %w", err)
	}
	absWorkspace = filepath.Clean(absWorkspace)

	typeName := strings.ToLower(strings.TrimSpace(storage.Type))
	if typeName == "" {
		typeName = knowl.StoreSQLite
	}
	switch typeName {
	case knowl.StoreSQLite:
		if storage.SQLite == nil {
			return StorageSettings{}, fmt.Errorf("knowl.storage.sqlite is required for type %q", knowl.StoreSQLite)
		}
		if storage.Postgres != nil {
			return StorageSettings{}, fmt.Errorf("knowl.storage.postgres must be omitted for type %q", knowl.StoreSQLite)
		}
		path := strings.TrimSpace(storage.SQLite.Path)
		if path == "" {
			path = filepath.Join(absWorkspace, ".knowl", "knowl.sqlite")
		} else if !filepath.IsAbs(path) {
			path = filepath.Join(absWorkspace, path)
		}
		return StorageSettings{Driver: knowl.StoreSQLite, Path: filepath.Clean(path)}, nil
	case knowl.StorePostgres:
		if storage.Postgres == nil {
			return StorageSettings{}, fmt.Errorf("knowl.storage.postgres is required for type %q", knowl.StorePostgres)
		}
		if storage.SQLite != nil {
			return StorageSettings{}, fmt.Errorf("knowl.storage.sqlite must be omitted for type %q", knowl.StorePostgres)
		}
		dsn := strings.TrimSpace(storage.Postgres.DSN)
		if dsn == "" {
			return StorageSettings{}, fmt.Errorf("knowl.storage.postgres.dsn is required for type %q", knowl.StorePostgres)
		}
		return StorageSettings{Driver: knowl.StorePostgres, DSN: dsn}, nil
	default:
		return StorageSettings{}, fmt.Errorf("unsupported knowl.storage.type %q", typeName)
	}
}

// ServerConfig controls the Knowl HTTP listener.
type ServerConfig struct {
	ListenAddr string `mapstructure:"listen_addr"`
}

// IngestConfig controls whether normal ingest stops for review or auto-applies.
// A nil pointer preserves the default review-first behavior at normalization
// time.
type IngestConfig struct {
	AutoApply *bool `mapstructure:"auto_apply"`
}

// MaintenanceConfig controls ingest review behavior. Pointers preserve the
// distinction between an omitted setting and an explicit false value while the
// document is being normalized.
type MaintenanceConfig struct {
	AutoApply *bool `mapstructure:"auto_apply"`
	Review    *bool `mapstructure:"review"`
}

type rawAppConfig struct {
	Provider    string              `mapstructure:"provider"`
	Workspace   WorkspaceConfig     `mapstructure:"workspace"`
	Storage     StorageConfig       `mapstructure:"storage"`
	Scope       knowltypes.ScopeRef `mapstructure:"scope"`
	Server      ServerConfig        `mapstructure:"server"`
	Ingest      IngestConfig        `mapstructure:"ingest"`
	Maintenance MaintenanceConfig   `mapstructure:"maintenance"`
}

// Normalize resolves legacy config aliases into the canonical public shape.
func (config rawAppConfig) Normalize() (AppConfig, error) {
	ingest, err := normalizeIngestConfig(config.Ingest, config.Maintenance)
	if err != nil {
		return AppConfig{}, err
	}
	return AppConfig{
		Provider:  config.Provider,
		Workspace: config.Workspace,
		Storage:   config.Storage,
		Scope:     config.Scope,
		Server:    config.Server,
		Ingest:    ingest,
	}, nil
}

func normalizeIngestConfig(canonical IngestConfig, legacy MaintenanceConfig) (IngestConfig, error) {
	if canonical.configured() {
		if legacy.configured() {
			return IngestConfig{}, fmt.Errorf("knowl.ingest.auto_apply cannot be combined with legacy knowl.maintenance settings")
		}
		return canonical, nil
	}
	if !legacy.configured() {
		return IngestConfig{}, nil
	}
	if legacy.AutoApply != nil && legacy.Review != nil && *legacy.AutoApply != !*legacy.Review {
		return IngestConfig{}, fmt.Errorf("knowl.maintenance.auto_apply conflicts with knowl.maintenance.review")
	}
	if legacy.AutoApply != nil {
		return IngestConfig{AutoApply: legacy.AutoApply}, nil
	}
	return IngestConfig{AutoApply: boolPtr(!*legacy.Review)}, nil
}

func (config IngestConfig) configured() bool {
	return config.AutoApply != nil
}

func (config MaintenanceConfig) configured() bool {
	return config.AutoApply != nil || config.Review != nil
}

func boolPtr(value bool) *bool {
	return &value
}
