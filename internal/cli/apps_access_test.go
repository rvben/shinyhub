package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestAppsAccessSubcommandsRegistered(t *testing.T) {
	apps := newAppsCmd()
	var access *cobra.Command
	for _, c := range apps.Commands() {
		if c.Name() == "access" {
			access = c
		}
	}
	if access == nil {
		t.Fatal("apps access command not registered")
	}
	want := map[string]bool{"set": false, "grant": false, "revoke": false, "list": false}
	for _, c := range access.Commands() {
		if _, ok := want[c.Name()]; ok {
			want[c.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("apps access %q subcommand not registered", name)
		}
	}
	var grant *cobra.Command
	for _, c := range access.Commands() {
		if c.Name() == "grant" {
			grant = c
		}
	}
	if grant == nil || grant.Flags().Lookup("role") == nil {
		t.Error("apps access grant must expose a --role flag")
	}
}
