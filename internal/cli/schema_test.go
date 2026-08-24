package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
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
	if doc.Clispec != "0.3" || doc.Name != "shinyhub" {
		t.Errorf("clispec=%q name=%q", doc.Clispec, doc.Name)
	}
	if doc.Version == "" {
		t.Error("version must be set")
	}
	if doc.Output.TTY != "table" || doc.Output.Piped != "json" || doc.Output.CI != "table" {
		t.Errorf("output defaults = %+v", doc.Output)
	}
	if len(doc.Output.CIEnvVars) != 2 || doc.Output.CIEnvVars[0] != "CI" || doc.Output.CIEnvVars[1] != "GITLAB_CI" {
		t.Errorf("CI output markers = %v", doc.Output.CIEnvVars)
	}
	if len(doc.Errors) != len(kindTable) {
		t.Errorf("errors len = %d, want %d", len(doc.Errors), len(kindTable))
	}
	// global_args: -o/--output, -q/--quiet and --no-color only; --config is
	// per-command.
	var names []string
	for _, a := range doc.GlobalArgs {
		names = append(names, a.Name)
	}
	want := map[string]bool{"--output": true, "--quiet": true, "--no-color": true}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected global arg %q", n)
		}
	}
	if len(names) != len(want) {
		t.Errorf("global_args = %v, want exactly %d", names, len(want))
	}
	// --output must carry its three valid values as an enum.
	found := false
	for _, a := range doc.GlobalArgs {
		if a.Name == "--output" {
			found = true
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
			break
		}
	}
	if !found {
		t.Error("--output not found in global_args")
	}
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
	list := findCommand(t, doc.Commands, "apps list")
	if list.Effects != "read_only" || list.Cardinality != "unbounded" {
		t.Errorf("apps list contract = effects %q, cardinality %q", list.Effects, list.Cardinality)
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

func TestGenerateSchema_BoundedOutputContractsResolve(t *testing.T) {
	doc := generateSchema(testRoot())
	globalArgs := map[string]bool{}
	for _, arg := range doc.GlobalArgs {
		globalArgs[arg.Name] = true
	}
	for _, command := range doc.Commands {
		if command.OutputKind == "stream" {
			if command.Cardinality != "" || command.Pagination != nil || command.FieldsArg != "" {
				t.Errorf("stream %q declares data bounds: %+v", command.Name, command)
			}
			continue
		}
		if command.Cardinality == "" {
			t.Errorf("data command %q has no cardinality", command.Name)
			continue
		}
		if command.Cardinality != "unbounded" {
			if command.Pagination != nil || command.FieldsArg != "" {
				t.Errorf("%s command %q must not declare unbounded controls", command.Cardinality, command.Name)
			}
			continue
		}
		knownArgs := map[string]bool{}
		for name := range globalArgs {
			knownArgs[name] = true
		}
		for _, arg := range command.Args {
			knownArgs[arg.Name] = true
		}
		if command.Pagination == nil {
			t.Errorf("unbounded command %q has no pagination", command.Name)
			continue
		}
		for label, name := range map[string]string{
			"pagination.limit_arg":  command.Pagination.LimitArg,
			"pagination.offset_arg": command.Pagination.OffsetArg,
			"fields_arg":            command.FieldsArg,
		} {
			if !knownArgs[name] {
				t.Errorf("command %q %s=%q does not resolve to an argument", command.Name, label, name)
			}
		}
	}

	apply := findCommand(t, doc.Commands, "fleet apply")
	if apply.Cardinality != "bounded" || apply.Pagination != nil || apply.FieldsArg != "" {
		t.Errorf("fleet apply output bounds = cardinality %q pagination %+v fields %q", apply.Cardinality, apply.Pagination, apply.FieldsArg)
	}
}

func TestGenerateSchema_EffectsReflectRetryGuarantees(t *testing.T) {
	doc := generateSchema(testRoot())
	for path, want := range map[string]string{
		"apps list":     "read_only",
		"apps start":    "idempotent",
		"fleet apply":   "idempotent",
		"apps restart":  "non_idempotent",
		"tokens create": "non_idempotent",
		"plan":          "non_idempotent",
		"run":           "non_idempotent",
	} {
		if got := findCommand(t, doc.Commands, path).Effects; got != want {
			t.Errorf("%s effects = %q, want %q", path, got, want)
		}
	}
}

func TestGenerateSchema_UnannotatedCommandHasNoEffectsClaim(t *testing.T) {
	root := &cobra.Command{Use: "test", Short: "test root"}
	root.AddCommand(&cobra.Command{
		Use:   "future",
		Short: "A newly added command",
		RunE:  func(*cobra.Command, []string) error { return nil },
	})
	doc := generateSchema(root)
	command := findCommand(t, doc.Commands, "future")
	if command.Effects != "" {
		t.Fatalf("unannotated command effects = %q, want empty so v0.3 validation fails closed", command.Effects)
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
			if em["exit_code_passthrough"] != true {
				t.Error("job_failed must declare exit_code_passthrough=true")
			}
			return
		}
	}
	t.Error("job_failed kind missing")
}

func TestGenerateSchema_TokensCreateNameRequired(t *testing.T) {
	doc := generateSchema(testRoot())
	create := findCommand(t, doc.Commands, "tokens create")
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

// TestGenerateSchema_NoRawUseCharsInPositionalNames asserts that no emitted
// positional or flag name contains cobra Use-string punctuation. This catches
// future Use strings like "set <slug> <a|b|c>" before they reach the schema.
// TestGenerateSchema_StreamingAnnotationWired verifies that Streaming:true in
// the registry propagates to the emitted JSON document.
func TestGenerateSchema_StreamingAnnotationWired(t *testing.T) {
	doc := generateSchema(testRoot())
	logs := findCommand(t, doc.Commands, "apps logs")
	if logs.OutputKind != "stream" || logs.StreamFormat != "ndjson" {
		t.Errorf("apps logs output = kind %q, format %q", logs.OutputKind, logs.StreamFormat)
	}
}

func TestGenerateSchema_NoRawUseCharsInPositionalNames(t *testing.T) {
	doc := generateSchema(testRoot())
	bad := "|<>[] "
	var checkArgs func(cmdName string, args []schemaArg)
	checkArgs = func(cmdName string, args []schemaArg) {
		for _, a := range args {
			for _, ch := range bad {
				if strings.ContainsRune(a.Name, ch) {
					t.Errorf("command %q arg %q contains illegal char %q", cmdName, a.Name, ch)
				}
			}
		}
	}
	for _, c := range doc.Commands {
		checkArgs(c.Name, c.Args)
	}
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

// TestSchemaDocument_ValidatesAgainstClispecV03 validates the emitted
// document against the vendored published schema.
func TestSchemaDocument_ValidatesAgainstClispecV03(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	f, err := os.Open("testdata/clispec-v0.3.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rawSchema, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource("clispec-v0.3.json", rawSchema); err != nil {
		t.Fatal(err)
	}
	sch, err := compiler.Compile("clispec-v0.3.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(generateSchema(testRoot()))
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := sch.Validate(inst); err != nil {
		t.Fatalf("schema document does not validate against clispec v0.3: %v", err)
	}
}
