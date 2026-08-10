package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	apphost "github.com/baldaworks/knowl/internal/apps/knowl"
	"github.com/normahq/runtime/v2/appconfig"
)

//go:embed knowl.yaml
var defaultKnowlConfig []byte

type knowlConfigDocument struct {
	Runtime appconfig.RuntimeConfig `mapstructure:"runtime"`
	Knowl   apphost.AppConfig       `mapstructure:"knowl"`
}

type loadedConfig struct {
	Document   knowlConfigDocument
	WorkingDir string
	ConfigDir  string
	Profile    string
}

type loadedConfigContextKey struct{}

func loadConfig(cmdContext context.Context, configDir, profile string) (context.Context, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return cmdContext, fmt.Errorf("get working directory: %w", err)
	}

	var document knowlConfigDocument
	selectedProfile, err := appconfig.LoadConfigDocument(
		appconfig.RuntimeLoadOptions{
			WorkingDir: workingDir,
			ConfigDir:  strings.TrimSpace(configDir),
			Profile:    strings.TrimSpace(profile),
		},
		appconfig.AppLoadOptions{
			AppName:            appName,
			DefaultsYAML:       defaultKnowlConfig,
			UseDotConfigAppDir: true,
		},
		&document,
	)
	if err != nil {
		return cmdContext, err
	}
	if document.Knowl.Storage.Type == "" && document.Knowl.Storage.SQLite == nil && document.Knowl.Storage.Postgres == nil {
		document.Knowl.Storage = apphost.StorageConfig{
			Type:   apphost.StoreSQLite,
			SQLite: &apphost.SQLiteConfig{},
		}
	}

	return context.WithValue(cmdContext, loadedConfigContextKey{}, loadedConfig{
		Document:   document,
		WorkingDir: workingDir,
		ConfigDir:  strings.TrimSpace(configDir),
		Profile:    selectedProfile,
	}), nil
}

func configFromContext(ctx context.Context) (loadedConfig, error) {
	if ctx == nil {
		return loadedConfig{}, fmt.Errorf("knowl config context is required")
	}
	config, ok := ctx.Value(loadedConfigContextKey{}).(loadedConfig)
	if !ok {
		return loadedConfig{}, fmt.Errorf("knowl config is not loaded")
	}
	return config, nil
}

func workspacePath(ctx context.Context) (string, error) {
	config, err := configFromContext(ctx)
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(config.Document.Knowl.Workspace.Path)
	if path == "" {
		path = defaultWorkspace
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(config.WorkingDir, path)
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func storeDriver(ctx context.Context) (string, error) {
	storage, err := storageSettings(ctx)
	if err != nil {
		return "", err
	}
	return storage.Driver, nil
}

func storageSettings(ctx context.Context) (apphost.StorageSettings, error) {
	config, err := configFromContext(ctx)
	if err != nil {
		return apphost.StorageSettings{}, err
	}
	workspace, err := workspacePath(ctx)
	if err != nil {
		return apphost.StorageSettings{}, err
	}
	return config.Document.Knowl.Storage.Normalize(workspace)
}

func configOutputPath(config loadedConfig) string {
	root := config.WorkingDir
	if strings.TrimSpace(config.ConfigDir) != "" {
		root = config.ConfigDir
		if !filepath.IsAbs(root) {
			root = filepath.Join(config.WorkingDir, root)
		}
		return filepath.Join(root, appName, "config.yaml")
	}
	return filepath.Join(root, ".config", appName, "config.yaml")
}
