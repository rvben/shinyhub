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
	want := map[string]bool{
		"set": false, "grant": false, "revoke": false, "list": false,
		"group-grant": false, "group-revoke": false, "group-list": false,
	}
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
	var groupGrant *cobra.Command
	for _, c := range access.Commands() {
		switch c.Name() {
		case "grant":
			grant = c
		case "group-grant":
			groupGrant = c
		}
	}
	if grant == nil || grant.Flags().Lookup("role") == nil {
		t.Error("apps access grant must expose a --role flag")
	}
	if groupGrant == nil || groupGrant.Flags().Lookup("role") == nil {
		t.Error("apps access group-grant must expose a --role flag")
	}
}
