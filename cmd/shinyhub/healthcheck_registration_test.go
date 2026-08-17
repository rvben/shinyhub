package main

import "testing"

func TestRootCmdHasHealthcheckSubcommand(t *testing.T) {
	for _, sub := range buildRoot().Commands() {
		if sub.Name() == "healthcheck" {
			return
		}
	}
	t.Fatalf("rootCmd does not have a healthcheck subcommand; has: %v", buildRoot().Commands())
}
