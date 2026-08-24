package cli

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// schemaDoc is the clispec v0.3 document emitted by `shinyhub schema`.
type schemaDoc struct {
	Clispec     string          `json:"clispec"`
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description,omitempty"`
	Output      schemaOutput    `json:"output"`
	GlobalArgs  []schemaArg     `json:"global_args,omitempty"`
	EnvVars     []schemaEnvVar  `json:"env_vars,omitempty"`
	Commands    []schemaCommand `json:"commands"`
	Errors      []schemaError   `json:"errors"`
}

type schemaOutput struct {
	TTY       string   `json:"tty,omitempty"`
	Piped     string   `json:"piped"`
	CI        string   `json:"ci,omitempty"`
	CIEnvVars []string `json:"ci_env_vars,omitempty"`
}

// schemaEnvVar documents an environment variable that changes CLI behavior.
// SHINYHUB_HOST/SHINYHUB_TOKEN are the entire non-interactive (CI) auth story,
// so making them discoverable here removes the biggest CI onboarding cliff.
type schemaEnvVar struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Scope       string `json:"scope,omitempty"` // "client" or "server"
}

// clientEnvVars are the environment variables that affect the shinyhub CLI (and
// the one server-side var clients need to know about).
func clientEnvVars() []schemaEnvVar {
	return []schemaEnvVar{
		{Name: "SHINYHUB_HOST", Scope: "client", Description: "Server URL; overrides the saved host. Set together with SHINYHUB_TOKEN for non-interactive (CI) auth - no `shinyhub connect` needed."},
		{Name: "SHINYHUB_TOKEN", Scope: "client", Description: "API key or pre-shared deploy token; overrides the saved token. Skips interactive login."},
		{Name: "SHINYHUB_CREDENTIALS", Scope: "client", Description: "Path to the client credentials file (default ~/.config/shinyhub/config.json). Preferred over SHINYHUB_CONFIG; unambiguously refers to the CLI credentials, not the server-side serve --config."},
		{Name: "SHINYHUB_CONFIG", Scope: "client", Description: "Legacy alias for SHINYHUB_CREDENTIALS. Still fully supported; SHINYHUB_CREDENTIALS takes precedence when both are set."},
		{Name: "SHINYHUB_DEPLOY_TOKEN", Scope: "server", Description: "Configured on the server to enable pre-shared deploy-token auth; clients pass its value as SHINYHUB_TOKEN."},
		{Name: "CI", Scope: "client", Description: "When enabled, unflagged output defaults to the human-readable table used for CI job logs. Pass --output json or --output ndjson for machine consumption."},
		{Name: "GITLAB_CI", Scope: "client", Description: "GitLab CI marker; also selects human-readable table output by default and enables automatic GitLab fleet provenance."},
		{Name: "NO_COLOR", Scope: "client", Description: "Set to any non-empty value to disable colored output, equivalent to --no-color. Color is already off whenever output is not a terminal, so piped and redirected output is never colored."},
		{Name: "CLICOLOR_FORCE", Scope: "client", Description: "Set to a non-zero value to emit color even when output is not a terminal (for CI logs that render ANSI). FORCE_COLOR is accepted as an alias; NO_COLOR and --no-color still win."},
	}
}

type schemaCommand struct {
	Name                  string            `json:"name"`
	Description           string            `json:"description"`
	Effects               string            `json:"effects"`
	Stability             string            `json:"stability,omitempty"`
	Args                  []schemaArg       `json:"args,omitempty"`
	OutputKind            string            `json:"output_kind"`
	Cardinality           string            `json:"cardinality,omitempty"`
	Pagination            *schemaPagination `json:"pagination,omitempty"`
	FieldsArg             string            `json:"fields_arg,omitempty"`
	ConfirmationBypassArg string            `json:"confirmation_bypass_arg,omitempty"`
	OutputFields          []fieldSpec       `json:"output_fields,omitempty"`
	StdoutSchema          *map[string]any   `json:"stdout_schema,omitempty"`
	EnvelopeFields        []fieldSpec       `json:"envelope_fields,omitempty"`
	StreamFormat          string            `json:"stream_format,omitempty"`
	ExitCodePassthrough   bool              `json:"exit_code_passthrough,omitempty"`
	Notes                 string            `json:"notes,omitempty"`
	Example               *schemaExample    `json:"example,omitempty"`
}

type schemaPagination struct {
	Style     string `json:"style"`
	LimitArg  string `json:"limit_arg"`
	OffsetArg string `json:"offset_arg"`
}

type schemaExample struct {
	Args []string `json:"args"`
}

type schemaArg struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Default     any      `json:"default,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Description string   `json:"description,omitempty"`
}

type schemaError struct {
	Kind                string `json:"kind"`
	ExitCode            int    `json:"exit_code,omitempty"`
	ExitCodePassthrough bool   `json:"exit_code_passthrough,omitempty"`
	Retryable           bool   `json:"retryable"`
	Description         string `json:"description,omitempty"`
}

// globalArgNames are root persistent flags advertised once at the top level.
// --config is deliberately excluded: serve/backup/restore shadow it with a
// different meaning, so the walk emits it per command instead.
var globalArgNames = map[string]bool{"output": true, "quiet": true, "no-color": true}

// GenerateSchemaDoc exposes schema generation for the cmd/shinyhub conformance
// tests, which exercise the full tree including server commands. The return
// type is any so schemaDoc stays unexported; callers consume it via
// json.Marshal.
func GenerateSchemaDoc(root *cobra.Command) any { return generateSchema(root) }

func generateSchema(root *cobra.Command) schemaDoc {
	doc := schemaDoc{
		Clispec:     "0.3",
		Name:        "shinyhub",
		Version:     version,
		Description: root.Short,
		Output:      schemaOutput{TTY: "table", Piped: "json", CI: "table", CIEnvVars: []string{"CI", "GITLAB_CI"}},
		EnvVars:     clientEnvVars(),
	}
	root.InitDefaultHelpFlag()
	root.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		if globalArgNames[f.Name] {
			doc.GlobalArgs = append(doc.GlobalArgs, flagToArg(f, ""))
		}
	})
	for _, c := range root.Commands() {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			continue
		}
		walkCommands(c, "", &doc.Commands)
	}
	for _, ki := range kindTable {
		doc.Errors = append(doc.Errors, schemaError{
			Kind: string(ki.Kind), ExitCode: ki.ExitCode, ExitCodePassthrough: ki.ExitCode == 0,
			Retryable: ki.Retryable, Description: ki.Desc,
		})
	}
	return doc
}

func walkCommands(c *cobra.Command, parentPath string, out *[]schemaCommand) {
	path := c.Name()
	if parentPath != "" {
		path = parentPath + " " + c.Name()
	}
	if c.Runnable() {
		*out = append(*out, schemaCommandFor(c, path))
	}
	for _, sub := range c.Commands() {
		if sub.Hidden || sub.Name() == "help" {
			continue
		}
		walkCommands(sub, path, out)
	}
}

func schemaCommandFor(c *cobra.Command, path string) schemaCommand {
	ann, annotated := schemaAnnotations[path]
	effects := ""
	if annotated {
		effects = effectsFor(path, ann)
	}
	sc := schemaCommand{
		Name:                path,
		Description:         c.Short,
		Effects:             effects,
		Stability:           ann.Stability,
		OutputKind:          "data",
		Cardinality:         ann.Cardinality,
		OutputFields:        v03Fields(ann.OutputFields),
		EnvelopeFields:      v03Fields(ann.EnvelopeFields),
		ExitCodePassthrough: ann.ExitCodePassthrough,
		Notes:               ann.Notes,
	}
	if ann.Streaming {
		sc.OutputKind = "stream"
		sc.StreamFormat = "ndjson"
	} else {
		if sc.Cardinality == "" {
			if len(ann.EnvelopeFields) > 0 {
				sc.Cardinality = "unbounded"
			} else {
				sc.Cardinality = "single"
			}
		}
		if sc.Cardinality == "unbounded" {
			sc.Pagination = &schemaPagination{Style: "offset", LimitArg: "--limit", OffsetArg: "--offset"}
			sc.FieldsArg = "--fields"
		}
	}
	if sc.OutputKind == "data" && len(sc.OutputFields) == 0 {
		empty := map[string]any{}
		sc.StdoutSchema = &empty
	}
	for _, pos := range positionalsFromUse(c.Use) {
		typ := "string"
		if ann.ArgTypes != nil && ann.ArgTypes[pos.name] != "" {
			typ = ann.ArgTypes[pos.name]
		}
		arg := schemaArg{Name: pos.name, Type: typ, Required: pos.required}
		if ann.ArgEnums != nil {
			arg.Enum = ann.ArgEnums[pos.name]
		}
		sc.Args = append(sc.Args, arg)
	}
	addFlag := func(f *pflag.Flag) {
		if f.Name == "help" || globalArgNames[f.Name] {
			return
		}
		sc.Args = append(sc.Args, flagToArg(f, path))
	}
	// Effective flags: locals (shadow winners) then inherited non-globals.
	seen := map[string]bool{}
	c.Flags().VisitAll(func(f *pflag.Flag) { // includes locals + persistent-on-self
		if !seen[f.Name] {
			seen[f.Name] = true
			addFlag(f)
		}
	})
	c.InheritedFlags().VisitAll(func(f *pflag.Flag) {
		if !seen[f.Name] {
			seen[f.Name] = true
			addFlag(f)
		}
	})
	if hasArg(sc.Args, "--yes") {
		sc.ConfirmationBypassArg = "--yes"
	}
	if path == "hosts" {
		sc.Example = &schemaExample{Args: []string{}}
	}
	return sc
}

func effectsFor(path string, ann cmdAnnotation) string {
	// These operations have regression coverage for an already-satisfied or
	// absent-state retry that exits successfully without repeating the effect.
	// Other mutating commands stay conservatively non-idempotent until they gain
	// the same guarantee; consumers must not infer retry safety from intent.
	idempotent := map[string]bool{
		"apps access revoke": true,
		"apps delete":        true,
		"apps start":         true,
		"apps stop":          true,
		"env set":            true,
		"fleet apply":        true,
		"logout":             true,
		"schedule add":       true,
		"share add":          true,
		"share rm":           true,
		"use":                true,
	}
	if idempotent[path] {
		return "idempotent"
	}
	// `plan --out` writes a time-bearing artifact and `run` launches a process,
	// so their v0.2 read-only marker is intentionally narrowed for v0.3.
	if path == "plan" || path == "run" {
		return "non_idempotent"
	}
	if ann.Mutating != nil && !*ann.Mutating {
		return "read_only"
	}
	return "non_idempotent"
}

func hasArg(args []schemaArg, name string) bool {
	for _, arg := range args {
		if arg.Name == name {
			return true
		}
	}
	return false
}

type positional struct {
	name     string
	required bool
}

// positionalsFromUse parses "<slug>" (required) and "[dir]" or "[<id>]"
// (optional) tokens out of a cobra Use string. Both bracket layers are
// stripped so "[<id>]" yields name "id", not "<id>".
func positionalsFromUse(use string) []positional {
	var out []positional
	for _, tok := range strings.Fields(use)[1:] {
		switch {
		case strings.HasPrefix(tok, "<") && strings.HasSuffix(tok, ">"):
			name := strings.Trim(tok, "<>")
			out = append(out, positional{name: name, required: true})
		case strings.HasPrefix(tok, "[") && strings.HasSuffix(tok, "]"):
			name := strings.Trim(strings.Trim(tok, "[]"), "<>")
			out = append(out, positional{name: name, required: false})
		}
	}
	return out
}

func flagToArg(f *pflag.Flag, cmdPath string) schemaArg {
	a := schemaArg{Name: "--" + f.Name, Description: f.Usage}
	switch f.Value.Type() {
	case "bool":
		a.Type = "boolean"
	case "int", "int64", "uint", "uint64":
		a.Type = "integer"
	case "stringSlice", "stringArray":
		a.Type = "string[]"
	case "duration":
		a.Type = "string"
		a.Description = strings.TrimSpace(a.Description + " (Go duration, e.g. 30s, 5m)")
	default:
		a.Type = "string"
	}
	if ann, ok := schemaAnnotations[cmdPath]; ok {
		if t := ann.ArgTypes["--"+f.Name]; t != "" {
			a.Type = t
		}
		if e := ann.ArgEnums["--"+f.Name]; len(e) > 0 {
			a.Enum = e
		}
	}
	// Fall back to the root annotation for inherited flags (e.g. --config ->
	// path, --output -> enum). Only applies when the command-path lookup above
	// did not already set a value.
	if rootAnn, ok := schemaAnnotations[""]; ok {
		if a.Type == "string" {
			if t := rootAnn.ArgTypes["--"+f.Name]; t != "" {
				a.Type = t
			}
		}
		if len(a.Enum) == 0 {
			if e := rootAnn.ArgEnums["--"+f.Name]; len(e) > 0 {
				a.Enum = e
			}
		}
	}
	if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" && f.DefValue != "[]" {
		a.Default = f.DefValue
	}
	if req, ok := f.Annotations[cobra.BashCompOneRequiredFlag]; ok && len(req) > 0 && req[0] == "true" {
		a.Required = true
	}
	return a
}
