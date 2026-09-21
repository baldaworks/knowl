package main

import (
	"encoding/json"
	"fmt"

	"github.com/baldaworks/knowl/internal/releaseinfo"
	"github.com/spf13/cobra"
)

const (
	versionCommandName  = "version"
	versionJSONFlagName = "json"
)

func newVersionCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   versionCommandName,
		Short: "Print the normalized Knowl build version as JSON",
		Long: "Print the normalized Knowl build version as JSON. Development builds report " +
			`{"version":"dev","release":false}; release builds also report the matching v-prefixed tag.`,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{skipConfigAnnotation: "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			identity, err := releaseinfo.Current()
			if err != nil {
				return fmt.Errorf("resolve build version: %w", err)
			}
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(identity); err != nil {
				return fmt.Errorf("encode build version: %w", err)
			}
			return nil
		},
	}
	command.Flags().Bool(versionJSONFlagName, false, "Print the version contract as JSON")
	return command
}
