package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/cli"
	"github.com/spf13/cobra"
)

func schemaJSON(t *testing.T) map[string]any {
	t.Helper()
	raw, err := json.Marshal(cli.GenerateSchemaDoc(buildRoot()))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// collectPaths walks the emitted document and returns every command path.
func collectPaths(prefix string, cmds []any, out map[string]map[string]any) {
	for _, c := range cmds {
		cm := c.(map[string]any)
		path := cm["name"].(string)
		if prefix != "" {
			path = prefix + " " + path
		}
		out[path] = cm
		if subs, ok := cm["subcommands"].([]any); ok {
			collectPaths(path, subs, out)
		}
	}
}

// TestSchema_EveryCommandHasExplicitMutating is the anti-drift gate: a new
// command without a registry annotation fails this test.
func TestSchema_EveryCommandHasExplicitMutating(t *testing.T) {
	doc := schemaJSON(t)
	paths := map[string]map[string]any{}
	collectPaths("", doc["commands"].([]any), paths)
	for _, required := range []string{"serve", "backup", "restore", "worker", "schema", "apps list", "fleet status"} {
		if _, ok := paths[required]; !ok {
			t.Errorf("command %q missing from schema document", required)
		}
	}
	for path, cm := range paths {
		if _, ok := cm["mutating"]; !ok {
			t.Errorf("command %q has no explicit mutating marker (v0.2: omitted means unknown)", path)
		}
	}
}

// TestSchema_TreeCoverage walks the live cobra tree and asserts every
// visible command appears in the document.
func TestSchema_TreeCoverage(t *testing.T) {
	doc := schemaJSON(t)
	paths := map[string]map[string]any{}
	collectPaths("", doc["commands"].([]any), paths)
	var walk func(c *cobra.Command, prefix string)
	walk = func(c *cobra.Command, prefix string) {
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			path := sub.Name()
			if prefix != "" {
				path = prefix + " " + sub.Name()
			}
			if _, ok := paths[path]; !ok {
				t.Errorf("live command %q missing from schema document", path)
			}
			walk(sub, path)
		}
	}
	walk(buildRoot(), "")
}

// TestRootHelp_MentionsSchema pins that the schema subcommand is visible in
// root help output.
func TestRootHelp_MentionsSchema(t *testing.T) {
	root := buildRoot()
	var out strings.Builder
	root.SetOut(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "schema") {
		t.Error("root --help does not mention the schema subcommand")
	}
}

// TestSchemaCommand_NoConfigNoNetwork runs the actual subcommand with a
// nonexistent config path and no network use; it must still succeed.
func TestSchemaCommand_NoConfigNoNetwork(t *testing.T) {
	t.Setenv("SHINYHUB_CONFIG", "/nonexistent/config.json")
	t.Setenv("SHINYHUB_HOST", "")
	t.Setenv("SHINYHUB_TOKEN", "")
	root := buildRoot()
	var out strings.Builder
	root.SetOut(&out)
	root.SetArgs([]string{"schema"})
	if err := root.Execute(); err != nil {
		t.Fatalf("schema must succeed with no config: %v", err)
	}
	if !strings.Contains(out.String(), `"clispec": "0.2"`) {
		t.Error("schema output missing clispec version marker")
	}
}
