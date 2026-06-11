package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// renderAction emits the uniform success envelope for mutating commands: a
// single JSON object in JSON mode, prose in table mode (suppressed by
// --quiet). JSON output is never suppressed by --quiet: it is the data.
func renderAction(cmd *cobra.Command, status string, fields map[string]any, prose string) error {
	format, err := resolveFormat(false, false)
	if err != nil {
		return err
	}
	return renderActionTo(cmd.OutOrStdout(), format, status, fields, prose)
}

// RenderAction is the exported form for use by cmd/shinyhub/main.go commands
// that live outside the cli package. It behaves identically to renderAction.
func RenderAction(cmd *cobra.Command, status string, fields map[string]any, prose string) error {
	return renderAction(cmd, status, fields, prose)
}

func renderActionTo(out io.Writer, format outputFormat, status string, fields map[string]any, prose string) error {
	if format == formatJSON {
		obj := map[string]any{"status": status}
		for k, v := range fields {
			obj[k] = v
		}
		return json.NewEncoder(out).Encode(obj)
	}
	if !quietFlag && prose != "" {
		fmt.Fprintln(out, prose)
	}
	return nil
}
