package cli

import (
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"
)

func testRoot() *cobra.Command {
	root := &cobra.Command{Use: "shinyhub", Short: "test root"}
	AddCommandsTo(root)
	return root
}

func TestGenerateSchema_TopLevel(t *testing.T) {
	doc := generateSchema(testRoot())
	if doc.Clispec != "0.2" || doc.Name != "shinyhub" {
		t.Errorf("clispec=%q name=%q", doc.Clispec, doc.Name)
	}
	if doc.Version == "" {
		t.Error("version must be set")
	}
	if len(doc.Errors) != len(kindTable) {
		t.Errorf("errors len = %d, want %d", len(doc.Errors), len(kindTable))
	}
	// global_args: -o/--output and -q/--quiet only; --config is per-command.
	var names []string
	for _, a := range doc.GlobalArgs {
		names = append(names, a.Name)
	}
	want := map[string]bool{"--output": true, "--quiet": true}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected global arg %q", n)
		}
	}
	if len(names) != 2 {
		t.Errorf("global_args = %v", names)
	}
}

func TestGenerateSchema_CommandsAndFlags(t *testing.T) {
	doc := generateSchema(testRoot())
	apps := findCommand(t, doc.Commands, "apps")
	list := findSub(t, apps, "list")
	if list.Mutating == nil || *list.Mutating {
		t.Error("apps list must be mutating=false in the document")
	}
	var hasJSON bool
	for _, a := range list.Args {
		if a.Name == "--json" {
			hasJSON = true
			if a.Type != "boolean" {
				t.Errorf("--json type = %q", a.Type)
			}
		}
		if a.Name == "--config" && a.Type != "path" {
			t.Errorf("--config should be type path, got %q", a.Type)
		}
	}
	if !hasJSON {
		t.Error("apps list --json flag missing from schema args")
	}
}

func TestGenerateSchema_JobFailedOmitsExitCode(t *testing.T) {
	doc := generateSchema(testRoot())
	raw, _ := json.Marshal(doc)
	var generic map[string]any
	_ = json.Unmarshal(raw, &generic)
	for _, e := range generic["errors"].([]any) {
		em := e.(map[string]any)
		if em["kind"] == "job_failed" {
			if _, has := em["exit_code"]; has {
				t.Error("job_failed must omit exit_code")
			}
			return
		}
	}
	t.Error("job_failed kind missing")
}

func TestGenerateSchema_TokensCreateNameRequired(t *testing.T) {
	doc := generateSchema(testRoot())
	tokens := findCommand(t, doc.Commands, "tokens")
	create := findSub(t, tokens, "create")
	for _, a := range create.Args {
		if a.Name == "--name" && a.Required {
			return
		}
	}
	t.Error("tokens create --name must have required=true in schema")
}

func findCommand(t *testing.T, cmds []schemaCommand, name string) schemaCommand {
	t.Helper()
	for _, c := range cmds {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("command %q not in schema", name)
	return schemaCommand{}
}

func findSub(t *testing.T, c schemaCommand, name string) schemaCommand {
	t.Helper()
	for _, s := range c.Subcommands {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("subcommand %q not in %q", name, c.Name)
	return schemaCommand{}
}
