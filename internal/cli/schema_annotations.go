package cli

// fieldSpec describes one field of a command's structured output.
type fieldSpec struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Desc string `json:"description,omitempty"`
}

// cmdAnnotation supplies what the cobra tree cannot know about a command.
// Mutating is a *bool because clispec v0.2 treats omitted as UNKNOWN; every
// command must state it explicitly. The conformance tests in cmd/shinyhub
// enforce full-tree coverage.
type cmdAnnotation struct {
	Mutating            *bool
	Stability           string // absent (empty) means unspecified; omitted from document
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
	// Root pseudo-entry: only ArgTypes and ArgEnums are read from this entry
	// (walkCommand never looks up ""); Mutating is not set here. Used to
	// propagate inherited-flag overrides to every command without repetition.
	"": {
		ArgTypes: map[string]string{"--config": "path"},
		ArgEnums: map[string][]string{"--output": {"table", "json", "ndjson"}},
	},

	// ── server-side commands ─────────────────────────────────────────────────
	"serve":   {Mutating: mut, Streaming: true},
	"backup":  {Mutating: mut, ArgTypes: map[string]string{"--out": "path"}},
	"restore": {Mutating: mut},
	"worker":  {Mutating: mut, Streaming: true},

	// ── auth ─────────────────────────────────────────────────────────────────
	"login":  {Mutating: mut},
	"logout": {Mutating: mut},

	// ── deploy ───────────────────────────────────────────────────────────────
	"deploy": {
		Mutating: mut,
		ArgEnums: map[string][]string{
			"--visibility": {"private", "shared", "public"},
		},
		OutputFields: []fieldSpec{
			{Name: "status", Type: "string", Desc: "deployed"},
			{Name: "slug", Type: "string"},
			{Name: "deploy_count", Type: "integer", Desc: "Cumulative deployment number"},
			{Name: "version", Type: "string", Desc: "Version string from the deployment"},
		},
	},

	// ── apps (container) ─────────────────────────────────────────────────────
	"apps": {Mutating: ro},

	"apps list": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "slug", Type: "string"},
		{Name: "status", Type: "string", Desc: "running | stopped | hibernated | failed"},
		{Name: "deploy_count", Type: "integer"},
	}, EnvelopeFields: []fieldSpec{
		{Name: "items", Type: "array"},
		{Name: "total", Type: "integer"},
		{Name: "limit", Type: "integer"},
		{Name: "offset", Type: "integer"},
	}},
	"apps show": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "slug", Type: "string"},
		{Name: "name", Type: "string"},
		{Name: "status", Type: "string"},
		{Name: "access", Type: "string"},
		{Name: "owner_id", Type: "integer"},
		{Name: "replicas", Type: "integer"},
		{Name: "max_sessions_per_replica", Type: "integer"},
		{Name: "deploy_count", Type: "integer"},
		{Name: "created_at", Type: "string"},
		{Name: "updated_at", Type: "string"},
		{Name: "autoscale_enabled", Type: "boolean"},
		{Name: "autoscale_min_replicas", Type: "integer"},
		{Name: "autoscale_max_replicas", Type: "integer"},
		{Name: "autoscale_target", Type: "number"},
		// Envelope-level fields returned alongside the app object.
		{Name: "replicas_status", Type: "array"},
		{Name: "effective_max_sessions_per_replica", Type: "integer"},
		{Name: "effective_autoscale_target", Type: "number"},
		{Name: "can_manage", Type: "boolean"},
		{Name: "autoscale_status", Type: "object"},
		{Name: "global_autoscale_enabled", Type: "boolean"},
	}},
	"apps logs": {Mutating: ro, Streaming: true},
	"apps deployments": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "id", Type: "integer"},
		{Name: "version", Type: "string"},
		{Name: "status", Type: "string"},
		{Name: "created_at", Type: "string"},
		{Name: "bundle_dir", Type: "string"},
		{Name: "content_digest", Type: "string"},
	}, EnvelopeFields: []fieldSpec{
		{Name: "items", Type: "array"},
		{Name: "total", Type: "integer"},
		{Name: "limit", Type: "integer"},
		{Name: "offset", Type: "integer"},
	}},
	"apps rollback": {Mutating: mut},
	"apps restart":  {Mutating: mut},
	"apps start":    {Mutating: mut},
	"apps stop":     {Mutating: mut},
	"apps delete":   {Mutating: mut},
	"apps set":      {Mutating: mut},

	// ── apps access (container) ───────────────────────────────────────────────
	"apps access": {Mutating: ro},

	"apps access set": {
		Mutating: mut,
		ArgEnums: map[string][]string{
			"level": {"private", "shared", "public"},
		},
	},
	"apps access grant": {
		Mutating: mut,
		ArgEnums: map[string][]string{
			"--role": {"viewer", "manager"},
		},
	},
	"apps access revoke": {Mutating: mut},
	"apps access list": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "user_id", Type: "integer"},
		{Name: "username", Type: "string"},
		{Name: "role", Type: "string"},
	}, EnvelopeFields: []fieldSpec{
		{Name: "items", Type: "array"},
		{Name: "total", Type: "integer"},
		{Name: "limit", Type: "integer"},
		{Name: "offset", Type: "integer"},
	}},
	"apps access group-grant": {
		Mutating: mut,
		ArgEnums: map[string][]string{
			"--role": {"viewer", "manager"},
		},
	},
	"apps access group-revoke": {Mutating: mut},
	"apps access group-list": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "group", Type: "string"},
		{Name: "role", Type: "string"},
	}, EnvelopeFields: []fieldSpec{
		{Name: "items", Type: "array"},
		{Name: "total", Type: "integer"},
		{Name: "limit", Type: "integer"},
		{Name: "offset", Type: "integer"},
	}},

	// ── tokens ───────────────────────────────────────────────────────────────
	"tokens": {Mutating: ro},

	"tokens create": {Mutating: mut},
	"tokens list": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "id", Type: "integer"},
		{Name: "name", Type: "string"},
		{Name: "created_at", Type: "string"},
	}, EnvelopeFields: []fieldSpec{
		{Name: "items", Type: "array"},
		{Name: "total", Type: "integer"},
		{Name: "limit", Type: "integer"},
		{Name: "offset", Type: "integer"},
	}},
	"tokens revoke": {Mutating: mut},

	// ── env ───────────────────────────────────────────────────────────────────
	"env": {Mutating: ro},

	"env set": {Mutating: mut},
	"env ls": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "key", Type: "string"},
		{Name: "value", Type: "string"},
		{Name: "secret", Type: "boolean"},
		{Name: "set", Type: "boolean"},
		{Name: "updated_at", Type: "integer"},
	}, EnvelopeFields: []fieldSpec{
		{Name: "items", Type: "array"},
		{Name: "total", Type: "integer"},
		{Name: "limit", Type: "integer"},
		{Name: "offset", Type: "integer"},
	}},
	"env rm":    {Mutating: mut},
	"env apply": {Mutating: mut},

	// ── data ─────────────────────────────────────────────────────────────────
	"data": {Mutating: ro},

	"data push": {Mutating: mut},
	"data ls": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "path", Type: "string"},
		{Name: "size", Type: "integer"},
		{Name: "sha256", Type: "string"},
		{Name: "modified_at", Type: "integer", Desc: "Unix timestamp"},
	}, EnvelopeFields: []fieldSpec{
		{Name: "items", Type: "array"},
		{Name: "total", Type: "integer"},
		{Name: "limit", Type: "integer"},
		{Name: "offset", Type: "integer"},
		{Name: "quota_mb", Type: "integer", Desc: "Storage quota in megabytes (0 = no quota)"},
		{Name: "used_bytes", Type: "integer", Desc: "Total bytes used across all files"},
	}},
	"data rm": {Mutating: mut},

	// ── schedule ─────────────────────────────────────────────────────────────
	"schedule": {Mutating: ro},

	"schedule ls": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "id", Type: "integer"},
		{Name: "name", Type: "string"},
		{Name: "cron_expr", Type: "string"},
		{Name: "command", Type: "array"},
		{Name: "enabled", Type: "boolean"},
		{Name: "timeout_seconds", Type: "integer"},
		{Name: "overlap_policy", Type: "string"},
		{Name: "missed_policy", Type: "string"},
		{Name: "effective_timezone", Type: "string"},
		{Name: "timezone_inherited", Type: "boolean"},
	}, EnvelopeFields: []fieldSpec{
		{Name: "items", Type: "array"},
		{Name: "total", Type: "integer"},
		{Name: "limit", Type: "integer"},
		{Name: "offset", Type: "integer"},
	}},
	"schedule add": {
		Mutating:            mut,
		ExitCodePassthrough: true,
	},
	"schedule update":  {Mutating: mut},
	"schedule rm":      {Mutating: mut},
	"schedule enable":  {Mutating: mut},
	"schedule disable": {Mutating: mut},
	"schedule run": {
		Mutating:            mut,
		ExitCodePassthrough: true,
	},
	"schedule logs": {
		Mutating:            ro,
		Streaming:           true,
		ExitCodePassthrough: true,
	},

	// ── share ─────────────────────────────────────────────────────────────────
	"share": {Mutating: ro},

	"share ls": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "source_slug", Type: "string"},
		{Name: "source_id", Type: "integer"},
	}, EnvelopeFields: []fieldSpec{
		{Name: "items", Type: "array"},
		{Name: "total", Type: "integer"},
		{Name: "limit", Type: "integer"},
		{Name: "offset", Type: "integer"},
	}},
	"share add": {Mutating: mut},
	"share rm":  {Mutating: mut},

	// ── fleet ─────────────────────────────────────────────────────────────────
	"fleet": {Mutating: ro},

	"fleet init":     {Mutating: mut},
	"fleet apply":    {Mutating: mut},
	"fleet validate": {Mutating: ro},
	"fleet plan":     {Mutating: ro},
	"fleet status": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "slug", Type: "string"},
		{Name: "managed_by", Type: "string"},
		{Name: "fleet_managed", Type: "boolean"},
		{Name: "content_digest", Type: "string"},
		{Name: "access", Type: "string"},
		{Name: "status", Type: "string"},
	}, EnvelopeFields: []fieldSpec{
		{Name: "items", Type: "array"},
		{Name: "schema_version", Type: "integer"},
		{Name: "total", Type: "integer"},
		{Name: "limit", Type: "integer"},
		{Name: "offset", Type: "integer"},
		{Name: "summary", Type: "object"},
	}},

	// ── manifest ──────────────────────────────────────────────────────────────
	"manifest":          {Mutating: ro},
	"manifest validate": {Mutating: ro},

	// ── schema ────────────────────────────────────────────────────────────────
	"schema": {Mutating: ro},
}
