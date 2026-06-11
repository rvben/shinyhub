package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// newSchemaCmd builds the clispec introspection command. The document is
// always JSON regardless of TTY (the schema IS the JSON document), so it
// bypasses resolveFormat and rejects explicit non-JSON formats. It needs no
// credentials, no config file, and no network.
func newSchemaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schema",
		Short: "Print a machine-readable description of this CLI (clispec v0.2)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if outputFlagValue != "" && outputFlagValue != string(formatJSON) {
				return validationErr(
					fmt.Sprintf("schema output is always JSON; --output %s is not supported", outputFlagValue),
					"drop --output or pass --output json")
			}
			doc := generateSchema(cmd.Root())
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(doc)
		},
	}
}
