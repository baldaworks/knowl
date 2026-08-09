package knowl

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"

	domain "github.com/baldaworks/knowl/pkg/knowl"
	"github.com/baldaworks/knowl/pkg/knowl/app"
)

const (
	StoreSQLite   = "sqlite"
	StorePostgres = "postgres"
	DefaultScope  = domain.ScopeRef("local")
	DefaultListen = "127.0.0.1:8080"
)

// AppConfig is the Knowl application section in the Balda-compatible config
// document. It is decoded before being normalized into the host Config.
type AppConfig struct {
	Provider    string            `mapstructure:"provider"`
	Workspace   WorkspaceConfig   `mapstructure:"workspace"`
	Storage     StorageConfig     `mapstructure:"storage"`
	Scope       domain.ScopeRef   `mapstructure:"scope"`
	Server      ServerConfig      `mapstructure:"server"`
	Operator    OperatorConfig    `mapstructure:"operator"`
	Maintenance MaintenanceConfig `mapstructure:"maintenance"`
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

// StorageSettings is the normalized selection consumed by the operational
// store opener. Only fields for the selected backend are populated.
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
		typeName = StoreSQLite
	}
	switch typeName {
	case StoreSQLite:
		if storage.SQLite == nil {
			return StorageSettings{}, fmt.Errorf("knowl.storage.sqlite is required for type %q", StoreSQLite)
		}
		if storage.Postgres != nil {
			return StorageSettings{}, fmt.Errorf("knowl.storage.postgres must be omitted for type %q", StoreSQLite)
		}
		path := strings.TrimSpace(storage.SQLite.Path)
		if path == "" {
			path = filepath.Join(absWorkspace, ".knowl", "knowl.sqlite")
		} else if !filepath.IsAbs(path) {
			path = filepath.Join(absWorkspace, path)
		}
		return StorageSettings{Driver: StoreSQLite, Path: filepath.Clean(path)}, nil
	case StorePostgres:
		if storage.Postgres == nil {
			return StorageSettings{}, fmt.Errorf("knowl.storage.postgres is required for type %q", StorePostgres)
		}
		if storage.SQLite != nil {
			return StorageSettings{}, fmt.Errorf("knowl.storage.sqlite must be omitted for type %q", StorePostgres)
		}
		dsn := strings.TrimSpace(storage.Postgres.DSN)
		if dsn == "" {
			return StorageSettings{}, fmt.Errorf("knowl.storage.postgres.dsn is required for type %q", StorePostgres)
		}
		return StorageSettings{Driver: StorePostgres, DSN: dsn}, nil
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

// MaintenanceConfig controls ingest review behavior. Pointers preserve the
// distinction between an omitted setting and an explicit false value while
// the document is being normalized.
type MaintenanceConfig struct {
	AutoApply *bool `mapstructure:"auto_apply"`
	Review    *bool `mapstructure:"review"`
}

// Config controls one local Knowl host. The scope is trusted host context.
type Config struct {
	Workspace       string
	Scope           domain.ScopeRef
	StoreDriver     string
	StorePath       string
	PostgresDSN     string
	ListenAddr      string
	OperatorToken   string
	ReadLimits      domain.ReadLimits
	IngestOptions   app.IngestOptions
	WorkerQueueSize int
	ShutdownTimeout time.Duration
}

// DefaultConfig returns conservative local defaults.
func DefaultConfig() Config {
	limits := app.DefaultReadLimits()
	return Config{
		Scope:           DefaultScope,
		StoreDriver:     StoreSQLite,
		ListenAddr:      DefaultListen,
		ReadLimits:      limits,
		IngestOptions:   app.IngestOptions{ReadLimits: limits},
		WorkerQueueSize: 16,
		ShutdownTimeout: 10 * time.Second,
	}
}

// Validate checks configuration without opening files, sockets, or databases.
func (config Config) Validate() error {
	_, err := config.normalized()
	return err
}

func (config Config) normalized() (Config, error) {
	if strings.TrimSpace(config.Workspace) == "" {
		return Config{}, fmt.Errorf("workspace path is required")
	}
	workspace, err := filepath.Abs(config.Workspace)
	if err != nil {
		return Config{}, fmt.Errorf("resolve workspace: %w", err)
	}
	config.Workspace = filepath.Clean(workspace)
	if strings.TrimSpace(string(config.Scope)) == "" {
		config.Scope = DefaultScope
	}
	config.Scope = domain.ScopeRef(strings.TrimSpace(string(config.Scope)))
	config.StoreDriver = strings.ToLower(strings.TrimSpace(config.StoreDriver))
	if config.StoreDriver == "" {
		config.StoreDriver = StoreSQLite
	}
	if config.StoreDriver != StoreSQLite && config.StoreDriver != StorePostgres {
		return Config{}, fmt.Errorf("unsupported store driver %q", config.StoreDriver)
	}
	if config.StoreDriver == StoreSQLite {
		if strings.TrimSpace(config.StorePath) == "" {
			config.StorePath = filepath.Join(config.Workspace, ".knowl", "knowl.sqlite")
		} else if !filepath.IsAbs(config.StorePath) {
			config.StorePath = filepath.Join(config.Workspace, config.StorePath)
		}
		config.StorePath = filepath.Clean(config.StorePath)
	} else if strings.TrimSpace(config.PostgresDSN) == "" {
		return Config{}, fmt.Errorf("postgres DSN is required for the postgres store")
	}
	if strings.TrimSpace(config.ListenAddr) == "" {
		config.ListenAddr = DefaultListen
	}
	if err := validateLoopbackAddr(config.ListenAddr); err != nil {
		return Config{}, err
	}
	if config.ReadLimits == (domain.ReadLimits{}) {
		config.ReadLimits = app.DefaultReadLimits()
	}
	if config.IngestOptions.ReadLimits == (domain.ReadLimits{}) {
		config.IngestOptions.ReadLimits = config.ReadLimits
	}
	if config.WorkerQueueSize <= 0 {
		config.WorkerQueueSize = 16
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = 10 * time.Second
	}
	return config, nil
}

func validateLoopbackAddr(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("listen address %q must include a port: %w", address, err)
	}
	if host == "localhost" {
		return nil
	}
	parsed := net.ParseIP(host)
	if parsed == nil || !parsed.IsLoopback() {
		return fmt.Errorf("listen address %q must be loopback", address)
	}
	return nil
}
