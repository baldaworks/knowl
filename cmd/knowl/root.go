package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	appName          = "knowl"
	initCommandName  = "init"
	postgresStore    = "postgres"
	configRelative   = ".config/knowl/config.yaml"
	workspaceKey     = "workspace.path"
	storeDriverKey   = "store.driver"
	defaultStore     = "sqlite"
	defaultWorkspace = "."
)

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   appName,
		Short: "Maintain a local Markdown knowledge wiki",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return loadConfig(cmd)
		},
	}
	root.PersistentFlags().String("config", "", "path to the Knowl config file")
	root.PersistentFlags().String("workspace", "", "workspace path")
	root.PersistentFlags().String("store-driver", "", "operational store driver (sqlite or postgres)")

	for _, command := range []*cobra.Command{
		newInitCommand(),
		newValidateCommand(),
		newStartCommand(),
		newIngestCommand(),
		newLintCommand(),
	} {
		root.AddCommand(command)
	}
	return root
}

func loadConfig(cmd *cobra.Command) error {
	workingDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	viper.Reset()
	viper.SetEnvPrefix("KNOWL")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	viper.AutomaticEnv()
	viper.SetDefault(workspaceKey, defaultWorkspace)
	viper.SetDefault(storeDriverKey, defaultStore)

	configPath, err := cmd.Flags().GetString("config")
	if err != nil {
		return fmt.Errorf("read config flag: %w", err)
	}
	if strings.TrimSpace(configPath) == "" {
		configPath = filepath.Join(workingDir, configRelative)
	}
	viper.SetConfigFile(configPath)
	if err := viper.ReadInConfig(); err != nil {
		_, statErr := os.Stat(configPath)
		if cmd.Name() != initCommandName || !os.IsNotExist(statErr) {
			return fmt.Errorf("read config %q: %w", configPath, err)
		}
	}
	if err := viper.BindPFlag(workspaceKey, cmd.Flags().Lookup("workspace")); err != nil {
		return fmt.Errorf("bind workspace flag: %w", err)
	}
	if err := viper.BindPFlag(storeDriverKey, cmd.Flags().Lookup("store-driver")); err != nil {
		return fmt.Errorf("bind store driver flag: %w", err)
	}
	return nil
}

func workspacePath() (string, error) {
	path := strings.TrimSpace(viper.GetString(workspaceKey))
	if path == "" {
		path = defaultWorkspace
	}
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get working directory: %w", err)
		}
		path = filepath.Join(cwd, path)
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func storeDriver() (string, error) {
	driver := strings.ToLower(strings.TrimSpace(viper.GetString(storeDriverKey)))
	if driver == "" {
		driver = defaultStore
	}
	if driver != defaultStore && driver != postgresStore {
		return "", fmt.Errorf("unsupported store.driver %q: want sqlite or postgres", driver)
	}
	return driver, nil
}
