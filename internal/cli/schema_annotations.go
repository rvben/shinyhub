package cli

// fieldSpec describes one field of a command's structured output.
type fieldSpec struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Desc string `json:"description,omitempty"`
}

// cmdAnnotation supplies what the cobra tree cannot know about a command.
// Mutating is a *bool because clispec v0.2 treats omitted as UNKNOWN; the
// registry must state it explicitly for every command (enforced by the
// coverage test in cmd/shinyhub).
type cmdAnnotation struct {
	Mutating            *bool
	Stability           string // "" means stable
	OutputFields        []fieldSpec
	EnvelopeFields      []fieldSpec       // list commands: envelope-level keys
	Streaming           bool              // stdout is a line stream (ndjson mode)
	ExitCodePassthrough bool              // schedule --follow family
	ArgTypes            map[string]string // flag/positional name -> type override (e.g. "path")
	ArgEnums            map[string][]string
	Notes               string // freeform extension note
}

func boolp(b bool) *bool { return &b }

var ro = boolp(false) // read-only
var mut = boolp(true) // mutating

// schemaAnnotations is keyed by command path: space-joined command names
// below the root, e.g. "apps list", "schedule add", "serve".
var schemaAnnotations = map[string]cmdAnnotation{
	// Root-level pseudo entry: type overrides for inherited flags that apply
	// to every command (e.g. --config is a filesystem path).
	"": {Mutating: ro, ArgTypes: map[string]string{"--config": "path"}},

	// apps list
	"apps list": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "slug", Type: "string"},
		{Name: "status", Type: "string", Desc: "running | stopped | hibernated | failed"},
		{Name: "deploy_count", Type: "integer"},
	}},
	"apps delete": {Mutating: mut},
}
