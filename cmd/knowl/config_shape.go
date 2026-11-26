package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
	knowltypes "github.com/baldaworks/knowl/pkg/knowl/types"
)

// AppConfig is the Knowl section of the Balda-compatible config document.
type AppConfig struct {
	Workers      int                       `mapstructure:"workers"`
	Provider     string                    `mapstructure:"provider"`
	Workspace    WorkspaceConfig           `mapstructure:"workspace"`
	Storage      StorageConfig             `mapstructure:"storage"`
	Scope        knowltypes.ScopeRef       `mapstructure:"scope"`
	DefaultScope knowltypes.ScopeName      `mapstructure:"default_scope"`
	Scopes       map[string]ScopeConfig    `mapstructure:"scopes"`
	Server       ServerConfig              `mapstructure:"server"`
	API          knowl.FacadeConfig        `mapstructure:"api"`
	MCP          knowl.FacadeConfig        `mapstructure:"mcp"`
	Web          knowl.WebConfig           `mapstructure:"web"`
	Operator     OperatorConfig            `mapstructure:"operator"`
	Sources      []SourceConfig            `mapstructure:"sources"`
	Embeddings   knowl.EmbeddingsConfig    `mapstructure:"embeddings"`
	Output       knowltypes.OutputSettings `mapstructure:"output"`
	Timeouts     knowl.TimeoutsConfig      `mapstructure:"timeouts"`
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

// OperatorConfig controls operator authentication.
type OperatorConfig struct {
	Token string `mapstructure:"token"`
}

// SourceConfig configures one named authoritative knowledge source.
type SourceConfig struct {
	ID         knowltypes.SourceID     `mapstructure:"id"`
	Type       knowltypes.SourceType   `mapstructure:"type"`
	Enabled    *bool                   `mapstructure:"enabled"`
	Filesystem *FilesystemSourceConfig `mapstructure:"filesystem"`
	Git        *GitSourceConfig        `mapstructure:"git"`
	Sync       SourceSyncConfig        `mapstructure:"sync"`
}

// ScopeConfig declares one public knowledge scope and its owned sources.
type ScopeConfig struct {
	Ref      knowltypes.ScopeRef `mapstructure:"ref"`
	Disabled bool                `mapstructure:"disabled"`
	Sources  []SourceConfig      `mapstructure:"sources"`
}

// FilesystemSourceConfig configures one local Markdown tree.
type FilesystemSourceConfig struct {
	Root    string   `mapstructure:"root"`
	Include []string `mapstructure:"include"`
	Flavor  string   `mapstructure:"flavor"`
	URIBase string   `mapstructure:"uri_base"`
}

// GitSourceConfig configures a remote Git repository source.
type GitSourceConfig struct {
	Remote           string        `mapstructure:"remote"`
	Ref              string        `mapstructure:"ref"`
	RefKind          string        `mapstructure:"ref_kind"`
	Include          []string      `mapstructure:"include"`
	Flavor           string        `mapstructure:"flavor"`
	URIBase          string        `mapstructure:"uri_base"`
	Auth             GitAuthConfig `mapstructure:"auth"`
	AllowRewrite     bool          `mapstructure:"allow_rewrite"`
	RebindAck        bool          `mapstructure:"rebind_ack"`
	KnownHosts       []string      `mapstructure:"known_hosts"`
	RepositoryID     string        `mapstructure:"repository_id"`
	MaxTransferBytes int64         `mapstructure:"max_transfer_bytes"`
	MaxCacheBytes    int64         `mapstructure:"max_cache_bytes"`
}

// GitAuthConfig configures credentials for Git source authentication.
type GitAuthConfig struct {
	SecretEnv string `mapstructure:"secret_env"`
	KeyFile   string `mapstructure:"key_file"`
}

// SourceSyncConfig controls on-start, periodic, and bounded retry scheduling.
type SourceSyncConfig struct {
	OnStart      bool          `mapstructure:"on_start"`
	Interval     time.Duration `mapstructure:"interval"`
	RetryInitial time.Duration `mapstructure:"retry_initial"`
	RetryMaximum time.Duration `mapstructure:"retry_maximum"`
	MaxDocuments *int          `mapstructure:"max_documents"`
}

type rawSourceConfig struct {
	ID         knowltypes.SourceID     `mapstructure:"id"`
	Type       knowltypes.SourceType   `mapstructure:"type"`
	Enabled    *bool                   `mapstructure:"enabled"`
	Filesystem *FilesystemSourceConfig `mapstructure:"filesystem"`
	Git        *GitSourceConfig        `mapstructure:"git"`
	Sync       rawSourceSyncConfig     `mapstructure:"sync"`
}

type rawScopeConfig struct {
	Ref      knowltypes.ScopeRef `mapstructure:"ref"`
	Disabled bool                `mapstructure:"disabled"`
	Sources  []rawSourceConfig   `mapstructure:"sources"`
}

type rawSourceSyncConfig struct {
	OnStart      bool          `mapstructure:"on_start"`
	Interval     time.Duration `mapstructure:"interval"`
	RetryInitial time.Duration `mapstructure:"retry_initial"`
	RetryMaximum time.Duration `mapstructure:"retry_maximum"`
	MaxDocuments any           `mapstructure:"max_documents"`
}

type rawAppConfig struct {
	Workers      any                       `mapstructure:"workers"`
	Provider     string                    `mapstructure:"provider"`
	Workspace    WorkspaceConfig           `mapstructure:"workspace"`
	Storage      StorageConfig             `mapstructure:"storage"`
	Scope        knowltypes.ScopeRef       `mapstructure:"scope"`
	DefaultScope knowltypes.ScopeName      `mapstructure:"default_scope"`
	Scopes       map[string]rawScopeConfig `mapstructure:"scopes"`
	Server       ServerConfig              `mapstructure:"server"`
	API          knowl.FacadeConfig        `mapstructure:"api"`
	MCP          knowl.FacadeConfig        `mapstructure:"mcp"`
	Web          knowl.WebConfig           `mapstructure:"web"`
	Operator     OperatorConfig            `mapstructure:"operator"`
	Sources      []rawSourceConfig         `mapstructure:"sources"`
	Embeddings   knowl.EmbeddingsConfig    `mapstructure:"embeddings"`
	Output       rawOutputSettings         `mapstructure:"output"`
	Timeouts     knowl.TimeoutsConfig      `mapstructure:"timeouts"`
	Ingest       map[string]any            `mapstructure:"ingest"`
	Maintenance  map[string]any            `mapstructure:"maintenance"`
}

type rawOutputSettings struct {
	MaxCorrections any `mapstructure:"max_corrections"`
}

func (settings rawOutputSettings) Normalize() (knowltypes.OutputSettings, error) {
	if settings.MaxCorrections == nil {
		return knowltypes.OutputSettings{}, nil
	}
	var allowance int
	switch value := settings.MaxCorrections.(type) {
	case int:
		allowance = value
	case string:
		switch value {
		case "0":
			allowance = 0
		case "1":
			allowance = 1
		default:
			return knowltypes.OutputSettings{}, app.ErrOutputCorrectionInvalid
		}
	default:
		return knowltypes.OutputSettings{}, app.ErrOutputCorrectionInvalid
	}
	output := knowltypes.OutputSettings{MaxCorrections: &allowance}
	if _, err := app.NormalizeOutputSettings(output); err != nil {
		return knowltypes.OutputSettings{}, err
	}
	return output, nil
}

// Normalize validates the public config shape and rejects removed compatibility
// sections.
func (config rawAppConfig) Normalize() (AppConfig, error) {
	workers := 1
	if config.Workers != nil {
		value, ok := config.Workers.(int)
		if !ok || value < 1 || value > 2 {
			return AppConfig{}, knowl.ErrWorkerConfigInvalid
		}
		workers = value
	}
	if len(config.Ingest) != 0 {
		return AppConfig{}, fmt.Errorf("knowl.ingest is not supported")
	}
	if len(config.Maintenance) != 0 {
		return AppConfig{}, fmt.Errorf("knowl.maintenance is not supported")
	}
	embeddings, err := config.Embeddings.Normalize()
	if err != nil {
		return AppConfig{}, err
	}
	output, err := config.Output.Normalize()
	if err != nil {
		return AppConfig{}, err
	}
	timeouts, err := config.Timeouts.Normalize()
	if err != nil {
		return AppConfig{}, err
	}
	sources, err := normalizeSourceConfigs(config.Sources)
	if err != nil {
		return AppConfig{}, err
	}
	scopes, err := normalizeScopeConfigs(config.Scopes)
	if err != nil {
		return AppConfig{}, err
	}
	return AppConfig{
		Workers:      workers,
		Output:       output,
		Embeddings:   embeddings,
		Provider:     config.Provider,
		Workspace:    config.Workspace,
		Storage:      config.Storage,
		Scope:        config.Scope,
		DefaultScope: config.DefaultScope,
		Scopes:       scopes,
		Server:       config.Server,
		API:          config.API,
		MCP:          config.MCP,
		Operator:     config.Operator,
		Web:          config.Web,
		Sources:      sources,
		Timeouts:     timeouts,
	}, nil
}

func normalizeSourceConfigs(configs []rawSourceConfig) ([]SourceConfig, error) {
	if len(configs) == 0 {
		return nil, nil
	}
	normalized := make([]SourceConfig, 0, len(configs))
	for _, config := range configs {
		maxDocuments, err := normalizeConfiguredMaxDocuments(config.Sync.MaxDocuments)
		if err != nil {
			return nil, fmt.Errorf("source %q sync.max_documents: %w", config.ID, err)
		}
		normalized = append(normalized, SourceConfig{
			ID: config.ID, Type: config.Type, Enabled: config.Enabled,
			Filesystem: config.Filesystem, Git: config.Git,
			Sync: SourceSyncConfig{
				OnStart: config.Sync.OnStart, Interval: config.Sync.Interval,
				RetryInitial: config.Sync.RetryInitial, RetryMaximum: config.Sync.RetryMaximum,
				MaxDocuments: maxDocuments,
			},
		})
	}
	return normalized, nil
}

func normalizeConfiguredMaxDocuments(value any) (*int, error) {
	if value == nil {
		return nil, nil
	}
	maxDocuments, ok := value.(int)
	if !ok {
		return nil, app.ErrSourceInvalid
	}
	if err := app.ValidateSourceMaxDocuments(maxDocuments); err != nil {
		return nil, err
	}
	return &maxDocuments, nil
}

func normalizeScopeConfigs(configs map[string]rawScopeConfig) (map[string]ScopeConfig, error) {
	if len(configs) == 0 {
		return nil, nil
	}
	normalized := make(map[string]ScopeConfig, len(configs))
	for name, config := range configs {
		sources, err := normalizeSourceConfigs(config.Sources)
		if err != nil {
			return nil, fmt.Errorf("scope %q: %w", name, err)
		}
		normalized[name] = ScopeConfig{Ref: config.Ref, Disabled: config.Disabled, Sources: sources}
	}
	return normalized, nil
}
