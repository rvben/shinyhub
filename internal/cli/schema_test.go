package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
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
	// --output must carry its three valid values as an enum.
	for _, a := range doc.GlobalArgs {
		if a.Name == "--output" {
			wantEnum := []string{"table", "json", "ndjson"}
			if len(a.Enum) != len(wantEnum) {
				t.Errorf("--output enum = %v, want %v", a.Enum, wantEnum)
				break
			}
			for i, v := range wantEnum {
				if a.Enum[i] != v {
					t.Errorf("--output enum[%d] = %q, want %q", i, a.Enum[i], v)
				}
			}
			return
		}
	}
	t.Error("--output not found in global_args")
}

func TestPositionalsFromUse(t *testing.T) {
	cases := []struct {
		use      string
		wantName string
		wantReq  bool
	}{
		{"revoke [<id>]", "id", false},
		{"update <slug> <name>", "slug", true},
		{"deploy [dir]", "dir", false},
	}
	for _, tc := range cases {
		t.Run(tc.use, func(t *testing.T) {
			got := positionalsFromUse(tc.use)
			if len(got) == 0 {
				t.Fatalf("positionalsFromUse(%q) returned nothing", tc.use)
			}
			if got[0].name != tc.wantName {
				t.Errorf("name = %q, want %q", got[0].name, tc.wantName)
			}
			if got[0].required != tc.wantReq {
				t.Errorf("required = %v, want %v", got[0].required, tc.wantReq)
			}
		})
	}
	// "update <slug> <name>" must yield both positionals.
	all := positionalsFromUse("update <slug> <name>")
	if len(all) != 2 || all[1].name != "name" || !all[1].required {
		t.Errorf("second positional = %+v, want {name:\"name\" required:true}", all)
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

func TestSchemaCommand_EmitsValidJSON(t *testing.T) {
	root := testRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"schema"})
	if err := root.Execute(); err != nil {
		t.Fatalf("schema command failed: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("schema output is not JSON: %v", err)
	}
}

func TestSchemaCommand_RejectsTableFormat(t *testing.T) {
	root := testRoot()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"schema", "-o", "table"})
	err := root.Execute()
	var ece *ExitCodeError
	if err == nil || !errors.As(err, &ece) || ece.Kind != KindValidation {
		t.Fatalf("want validation error, got %v", err)
	}
}

// TestSchemaDocument_ValidatesAgainstClispecV02 validates the emitted
// document against the vendored published schema.
func TestSchemaDocument_ValidatesAgainstClispecV02(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	f, err := os.Open("testdata/clispec-v0.2.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rawSchema, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource("clispec-v0.2.json", rawSchema); err != nil {
		t.Fatal(err)
	}
	sch, err := compiler.Compile("clispec-v0.2.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(generateSchema(testRoot()))
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := sch.Validate(inst); err != nil {
		t.Fatalf("schema document does not validate against clispec v0.2: %v", err)
	}
}
