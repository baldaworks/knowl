package knowl

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/baldaworks/knowl/pkg/knowl/app"
	domain "github.com/baldaworks/knowl/pkg/knowl/types"
)

// ErrWebConfigInvalid identifies an unsupported Web facade policy.
var ErrWebConfigInvalid = errors.New("invalid web facade configuration")

// ErrFacadeConfigInvalid identifies an unsupported API or MCP facade policy.
var ErrFacadeConfigInvalid = errors.New("invalid facade configuration")

// AuthMode controls how an HTTP facade authenticates requests.
type AuthMode string

const (
	AuthModePublic AuthMode = "public"
	AuthModeToken  AuthMode = "token"
	AuthModeProxy  AuthMode = "proxy"
)

// AccessMode controls the capabilities exposed by an HTTP facade.
type AccessMode string

const (
	AccessModeReadOnly  AccessMode = "read_only"
	AccessModeReadWrite AccessMode = "read_write"
)

// FacadeConfig controls authentication and capability independently.
type FacadeConfig struct {
	AuthMode   AuthMode   `mapstructure:"auth_mode"`
	AccessMode AccessMode `mapstructure:"access_mode"`
}

// WebConfig controls opt-in operator HTTP and UI access.
type WebConfig struct {
	Enabled    bool       `mapstructure:"enabled"`
	AuthMode   AuthMode   `mapstructure:"auth_mode"`
	AccessMode AccessMode `mapstructure:"access_mode"`
}

// ErrWorkerConfigInvalid identifies unsupported maintenance capacity or ownership.
var ErrWorkerConfigInvalid = errors.New("invalid maintenance worker configuration")

const (
	StoreSQLite   = "sqlite"
	StorePostgres = "postgres"
	DefaultScope  = domain.ScopeRef("local")
	DefaultListen = "127.0.0.1:8080"
)

// Config controls one local Knowl host. The scope is trusted host context.
type Config struct {
	Workspace     string
	Scope         domain.ScopeRef
	Sources       []domain.Source
	StoreDriver   string
	StorePath     string
	PostgresDSN   string
	ListenAddr    string
	OperatorToken string
	API           FacadeConfig
	MCP           FacadeConfig
	Web           WebConfig
	ReadLimits    domain.ReadLimits
	IngestOptions app.IngestOptions
	// Workers bounds simultaneous owned maintenance execution; zero defaults to one.
	Workers int
	// WorkerQueueSize bounds only coalesced in-memory wake hints. Accepted work
	// remains durable and is recovered by scheduler scans when hints are lost.
	WorkerQueueSize int
	ShutdownTimeout time.Duration
	Embeddings      EmbeddingsConfig
	Output          domain.OutputSettings
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
		Workers:         1,
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
	config.OperatorToken = strings.TrimSpace(config.OperatorToken)
	var err error
	config.API, err = normalizeFacade("api", config.API, config.OperatorToken, false)
	if err != nil {
		return Config{}, err
	}
	if config.API.AuthMode == AuthModeToken && config.OperatorToken == "" {
		return Config{}, fmt.Errorf("%w: api token auth requires operator.token", ErrFacadeConfigInvalid)
	}
	config.MCP, err = normalizeFacade("mcp", config.MCP, config.OperatorToken, false)
	if err != nil {
		return Config{}, err
	}
	if config.MCP.AuthMode == AuthModeToken && config.OperatorToken == "" {
		return Config{}, fmt.Errorf("%w: mcp token auth requires operator.token", ErrFacadeConfigInvalid)
	}
	webPolicy, err := normalizeFacade("web", FacadeConfig{AuthMode: config.Web.AuthMode, AccessMode: config.Web.AccessMode}, config.OperatorToken, true)
	if err != nil {
		return Config{}, fmt.Errorf("%w: %v", ErrWebConfigInvalid, err)
	}
	config.Web.AuthMode = webPolicy.AuthMode
	config.Web.AccessMode = webPolicy.AccessMode
	if config.Web.Enabled && (config.Web.AuthMode == AuthModeToken || config.Web.AuthMode == AuthModeProxy) && config.OperatorToken == "" {
		return Config{}, fmt.Errorf("%w: enabled %s mode requires operator.token", ErrWebConfigInvalid, config.Web.AuthMode)
	}
	if config.Workers == 0 {
		config.Workers = 1
	}
	if config.Workers < 1 || config.Workers > 2 {
		return Config{}, ErrWorkerConfigInvalid
	}
	if strings.TrimSpace(config.Workspace) == "" {
		return Config{}, fmt.Errorf("workspace path is required")
	}
	workspace, err := filepath.Abs(config.Workspace)
	if err != nil {
		return Config{}, fmt.Errorf("resolve workspace: %w", err)
	}
	config.Workspace = filepath.Clean(workspace)
	config.Embeddings, err = config.Embeddings.Normalize()
	if err != nil {
		return Config{}, err
	}
	if _, err := app.NormalizeOutputSettings(config.Output); err != nil {
		return Config{}, err
	}
	config.Sources, err = normalizeSources(config.Workspace, "", config.Sources)
	if err != nil {
		return Config{}, err
	}
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
	if err := validateListenAddr(config.ListenAddr); err != nil {
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

func normalizeFacade(name string, facade FacadeConfig, token string, allowProxy bool) (FacadeConfig, error) {
	facade.AuthMode = AuthMode(strings.ToLower(strings.TrimSpace(string(facade.AuthMode))))
	if facade.AuthMode == "" {
		facade.AuthMode = AuthModePublic
		if token != "" {
			facade.AuthMode = AuthModeToken
		}
	}
	switch facade.AuthMode {
	case AuthModePublic, AuthModeToken:
	case AuthModeProxy:
		if !allowProxy {
			return FacadeConfig{}, fmt.Errorf("%w: %s auth_mode %q is unsupported", ErrFacadeConfigInvalid, name, facade.AuthMode)
		}
	default:
		return FacadeConfig{}, fmt.Errorf("%w: %s auth_mode %q is unsupported", ErrFacadeConfigInvalid, name, facade.AuthMode)
	}

	facade.AccessMode = AccessMode(strings.ToLower(strings.TrimSpace(string(facade.AccessMode))))
	if facade.AccessMode == "" {
		facade.AccessMode = AccessModeReadOnly
	}
	switch facade.AccessMode {
	case AccessModeReadOnly:
	case AccessModeReadWrite:
		if allowProxy {
			return FacadeConfig{}, fmt.Errorf("%w: %s access_mode %q is unsupported", ErrFacadeConfigInvalid, name, facade.AccessMode)
		}
		if facade.AuthMode != AuthModeToken {
			return FacadeConfig{}, fmt.Errorf("%w: %s read_write requires token auth", ErrFacadeConfigInvalid, name)
		}
	default:
		return FacadeConfig{}, fmt.Errorf("%w: %s access_mode %q is unsupported", ErrFacadeConfigInvalid, name, facade.AccessMode)
	}
	return facade, nil
}

func validateListenAddr(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("listen address %q must include a port: %w", address, err)
	}
	if host == "" || host == "localhost" {
		return nil
	}
	parsed := net.ParseIP(host)
	if parsed == nil {
		return fmt.Errorf("listen address %q must use localhost, an empty host, or a literal IP", address)
	}
	return nil
}
