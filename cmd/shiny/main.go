package main

import (
	"os"

	"github.com/rvben/shinyhub/internal/cli"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{Use: "shiny", Short: "deprecated — use shinyhub"}
	cli.AddCommandsTo(root)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
