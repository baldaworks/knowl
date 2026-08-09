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
