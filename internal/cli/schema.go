package cli

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// schemaDoc is the clispec v0.2 document emitted by `shinyhub schema`.
type schemaDoc struct {
	Clispec     string          `json:"clispec"`
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description,omitempty"`
	GlobalArgs  []schemaArg     `json:"global_args,omitempty"`
	Commands    []schemaCommand `json:"commands"`
	Errors      []schemaError   `json:"errors"`
}

type schemaCommand struct {
	Name                string          `json:"name"`
	Description         string          `json:"description,omitempty"`
	Mutating            *bool           `json:"mutating,omitempty"`
	Stability           string          `json:"stability,omitempty"`
	Args                []schemaArg     `json:"args,omitempty"`
	OutputFields        []fieldSpec     `json:"output_fields,omitempty"`
	EnvelopeFields      []fieldSpec     `json:"envelope_fields,omitempty"`
	Streaming           bool            `json:"streaming,omitempty"`
	ExitCodePassthrough bool            `json:"exit_code_passthrough,omitempty"`
	Notes               string          `json:"notes,omitempty"`
	Subcommands         []schemaCommand `json:"subcommands,omitempty"`
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
	Kind        string `json:"kind"`
	ExitCode    int    `json:"exit_code,omitempty"`
	Retryable   bool   `json:"retryable"`
	Description string `json:"description,omitempty"`
}

// globalArgNames are root persistent flags advertised once at the top level.
// --config is deliberately excluded: serve/backup/restore shadow it with a
// different meaning, so the walk emits it per command instead.
var globalArgNames = map[string]bool{"output": true, "quiet": true}

func generateSchema(root *cobra.Command) schemaDoc {
	doc := schemaDoc{
		Clispec:     "0.2",
		Name:        "shinyhub",
		Version:     version,
		Description: root.Short,
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
		doc.Commands = append(doc.Commands, walkCommand(c, ""))
	}
	for _, ki := range kindTable {
		doc.Errors = append(doc.Errors, schemaError{
			Kind: string(ki.Kind), ExitCode: ki.ExitCode,
			Retryable: ki.Retryable, Description: ki.Desc,
		})
	}
	return doc
}

func walkCommand(c *cobra.Command, parentPath string) schemaCommand {
	path := c.Name()
	if parentPath != "" {
		path = parentPath + " " + c.Name()
	}
	ann := schemaAnnotations[path]
	sc := schemaCommand{
		Name:                c.Name(),
		Description:         c.Short,
		Mutating:            ann.Mutating,
		Stability:           ann.Stability,
		OutputFields:        ann.OutputFields,
		EnvelopeFields:      ann.EnvelopeFields,
		Streaming:           ann.Streaming,
		ExitCodePassthrough: ann.ExitCodePassthrough,
		Notes:               ann.Notes,
	}
	for _, pos := range positionalsFromUse(c.Use) {
		typ := "string"
		if ann.ArgTypes != nil && ann.ArgTypes[pos.name] != "" {
			typ = ann.ArgTypes[pos.name]
		}
		sc.Args = append(sc.Args, schemaArg{Name: pos.name, Type: typ, Required: pos.required})
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
	for _, sub := range c.Commands() {
		if sub.Hidden || sub.Name() == "help" {
			continue
		}
		sc.Subcommands = append(sc.Subcommands, walkCommand(sub, path))
	}
	return sc
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
