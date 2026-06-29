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

	// ── local dev runner ─────────────────────────────────────────────────────
	"run": {Mutating: ro, Streaming: true,
		ArgTypes: map[string]string{"--data-dir": "path", "--env-file": "path"},
		OutputFields: []fieldSpec{
			{Name: "slug", Type: "string"},
			{Name: "url", Type: "string"},
			{Name: "port", Type: "integer"},
			{Name: "status", Type: "string"},
		}},

	// ── server-side commands ─────────────────────────────────────────────────
	"serve": {Mutating: mut, Streaming: true},
	"backup": {Mutating: mut, ArgTypes: map[string]string{"--out": "path"}, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "written"},
		{Name: "path", Type: "string"},
	}},
	"restore": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "restored"},
		{Name: "archive", Type: "string"},
	}},
	"worker": {Mutating: mut, Streaming: true},

	// ── auth ─────────────────────────────────────────────────────────────────
	"login":  {Mutating: mut},
	"logout": {Mutating: mut},
	"whoami": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "ok"},
		{Name: "username", Type: "string"},
		{Name: "role", Type: "string"},
		{Name: "host", Type: "string", Desc: "Server URL the credentials target"},
		{Name: "can_create_apps", Type: "boolean"},
	}},

	// ── deploy ───────────────────────────────────────────────────────────────
	"deploy": {
		Mutating: mut,
		ArgEnums: map[string][]string{
			"--visibility": {"private", "shared", "public"},
		},
		OutputFields: []fieldSpec{
			{Name: "status", Type: "string", Desc: "deployed"},
			{Name: "slug", Type: "string"},
			{Name: "deploy_count", Type: "integer", Desc: "Cumulative deployment number; 0 when the server does not report one"},
			{Name: "version", Type: "string", Desc: "Version string from the deployment; empty when the server does not report one"},
		},
		Notes: "shinyhub.toml [app] startup_timeout_seconds (1-3600, default 120) sets the readiness deadline the deploy health check allows before declaring the app crashed; it travels with the bundle and also applies on wake, scale, and rollback.",
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
	"apps metrics": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "status", Type: "string"},
		{Name: "sessions_cap", Type: "integer"},
		{Name: "metrics_available", Type: "boolean"},
		{Name: "cpu_percent", Type: "number", Desc: "Aggregate CPU% across replicas"},
		{Name: "rss_bytes", Type: "integer", Desc: "Aggregate resident memory across replicas"},
		{Name: "replicas", Type: "array", Desc: "Per-replica index, status, pid, cpu_percent, rss_bytes, sessions"},
	}},
	"apps deployments": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "id", Type: "integer"},
		{Name: "version", Type: "string"},
		{Name: "release_number", Type: "integer", Desc: "Human-friendly v1/v2/… rank among succeeded deploys; null for failed/pending"},
		{Name: "status", Type: "string"},
		{Name: "failure_reason", Type: "string", Desc: "Why a failed deploy failed; empty for pending/succeeded"},
		{Name: "created_at", Type: "string"},
		{Name: "bundle_dir", Type: "string"},
		{Name: "content_digest", Type: "string"},
	}, EnvelopeFields: []fieldSpec{
		{Name: "items", Type: "array"},
		{Name: "total", Type: "integer"},
		{Name: "limit", Type: "integer"},
		{Name: "offset", Type: "integer"},
	}},
	"apps rollback": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "rolled_back"},
		{Name: "slug", Type: "string"},
		{Name: "deployment_id", Type: "integer", Desc: "Target deployment ID when --to is specified"},
	}},
	"apps restart": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "running"},
		{Name: "slug", Type: "string"},
	}},
	"apps start": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "running"},
		{Name: "slug", Type: "string"},
		{Name: "note", Type: "string", Desc: "Present on already-running no-op; value is already running"},
	}},
	"apps stop": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "stopped"},
		{Name: "slug", Type: "string"},
	}},
	"apps delete": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "deleted | absent"},
		{Name: "slug", Type: "string"},
	}},
	"apps set": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "updated"},
		{Name: "slug", Type: "string"},
	}},

	// ── apps access (container) ───────────────────────────────────────────────
	"apps access": {Mutating: ro},

	"apps access set": {
		Mutating: mut,
		ArgEnums: map[string][]string{
			"level": {"private", "shared", "public"},
		},
		OutputFields: []fieldSpec{
			{Name: "status", Type: "string", Desc: "updated"},
			{Name: "slug", Type: "string"},
			{Name: "access", Type: "string"},
		},
	},
	"apps access grant": {
		Mutating: mut,
		ArgEnums: map[string][]string{
			"--role": {"viewer", "manager"},
		},
		OutputFields: []fieldSpec{
			{Name: "status", Type: "string", Desc: "granted"},
			{Name: "slug", Type: "string"},
			{Name: "username", Type: "string"},
			{Name: "role", Type: "string", Desc: "Present when --role was specified"},
		},
	},
	"apps access revoke": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "revoked"},
		{Name: "slug", Type: "string"},
		{Name: "username", Type: "string"},
	}},
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
		OutputFields: []fieldSpec{
			{Name: "status", Type: "string", Desc: "granted"},
			{Name: "slug", Type: "string"},
			{Name: "group", Type: "string"},
			{Name: "role", Type: "string"},
		},
	},
	"apps access group-revoke": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "revoked"},
		{Name: "slug", Type: "string"},
		{Name: "group", Type: "string"},
	}},
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

	"tokens create": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "id", Type: "integer"},
		{Name: "name", Type: "string"},
		{Name: "token", Type: "string", Desc: "The token value (shown once)"},
		{Name: "created_at", Type: "string"},
	}},
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
	"tokens revoke": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "revoked"},
		{Name: "token_id", Type: "string"},
	}},

	// ── env ───────────────────────────────────────────────────────────────────
	"env": {Mutating: ro},

	"env set": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "set | unchanged"},
		{Name: "slug", Type: "string"},
		{Name: "key", Type: "string"},
	}},
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
	"env rm": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "removed"},
		{Name: "slug", Type: "string"},
		{Name: "key", Type: "string"},
	}},
	"env apply": {Mutating: mut},

	// ── data ─────────────────────────────────────────────────────────────────
	"data": {Mutating: ro},

	"data push": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "uploaded, or planned with --dry-run"},
		{Name: "slug", Type: "string"},
		{Name: "path", Type: "string", Desc: "Effective destination inside the data dir"},
		{Name: "local", Type: "string", Desc: "Local source file"},
		{Name: "bytes", Type: "integer", Desc: "File size in bytes"},
		{Name: "dry_run", Type: "boolean", Desc: "Present and true when --dry-run skipped the upload"},
	}},
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
	"data rm": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "removed"},
		{Name: "slug", Type: "string"},
		{Name: "path", Type: "string"},
	}},

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
	"schedule runs": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "id", Type: "integer"},
		{Name: "schedule_id", Type: "integer"},
		{Name: "status", Type: "string", Desc: "running | succeeded | failed | timed_out | cancelled | interrupted | skipped_overlap"},
		{Name: "trigger", Type: "string", Desc: "cron | manual | register"},
		{Name: "exit_code", Type: "integer", Desc: "null while running or interrupted; the command's exit code once finished"},
		{Name: "started_at", Type: "string"},
		{Name: "finished_at", Type: "string"},
	}, EnvelopeFields: []fieldSpec{
		{Name: "items", Type: "array"},
		{Name: "total", Type: "integer"},
		{Name: "limit", Type: "integer"},
		{Name: "offset", Type: "integer"},
	}},
	"schedule add": {
		Mutating:            mut,
		ExitCodePassthrough: true,
		OutputFields: []fieldSpec{
			{Name: "status", Type: "string", Desc: "created | unchanged"},
			{Name: "slug", Type: "string"},
			{Name: "name", Type: "string"},
			{Name: "id", Type: "integer"},
			{Name: "first_fire_run_id", Type: "integer", Desc: "Present when --run-on-register triggered a first run"},
		},
	},
	"schedule update": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "updated"},
		{Name: "slug", Type: "string"},
		{Name: "name", Type: "string"},
	}},
	"schedule rm": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "removed"},
		{Name: "slug", Type: "string"},
		{Name: "name", Type: "string"},
	}},
	"schedule enable": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "enabled"},
		{Name: "slug", Type: "string"},
		{Name: "name", Type: "string"},
	}},
	"schedule disable": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "disabled"},
		{Name: "slug", Type: "string"},
		{Name: "name", Type: "string"},
	}},
	"schedule run": {
		Mutating:            mut,
		ExitCodePassthrough: true,
		OutputFields: []fieldSpec{
			{Name: "status", Type: "string", Desc: "started"},
			{Name: "slug", Type: "string"},
			{Name: "name", Type: "string"},
		},
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
	"share add": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "mounted"},
		{Name: "slug", Type: "string"},
		{Name: "source_slug", Type: "string"},
	}},
	"share rm": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "unmounted"},
		{Name: "slug", Type: "string"},
		{Name: "source_slug", Type: "string"},
	}},

	// ── users (admin) ─────────────────────────────────────────────────────────
	"users": {Mutating: ro},

	"users list": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "id", Type: "integer"},
		{Name: "username", Type: "string"},
		{Name: "role", Type: "string", Desc: "viewer | developer | operator | admin"},
		{Name: "created_at", Type: "string"},
	}, EnvelopeFields: []fieldSpec{
		{Name: "items", Type: "array"},
		{Name: "total", Type: "integer"},
		{Name: "limit", Type: "integer"},
		{Name: "offset", Type: "integer"},
	}},
	"users create": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "created"},
		{Name: "id", Type: "integer"},
		{Name: "username", Type: "string"},
		{Name: "role", Type: "string"},
	}, ArgEnums: map[string][]string{
		"--role": {"viewer", "developer", "operator", "admin"},
	}},
	"users set-role": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "role_updated"},
		{Name: "id", Type: "integer"},
		{Name: "username", Type: "string"},
		{Name: "role", Type: "string"},
	}, ArgEnums: map[string][]string{
		"--role": {"viewer", "developer", "operator", "admin"},
	}},
	"users reset-password": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "password_reset"},
		{Name: "id", Type: "integer"},
		{Name: "username", Type: "string"},
	}},
	"users delete": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "deleted"},
		{Name: "id", Type: "integer"},
		{Name: "username", Type: "string"},
	}},

	// ── fleet ─────────────────────────────────────────────────────────────────
	"fleet": {Mutating: ro},

	"fleet init": {Mutating: mut},
	"fleet apply": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "failure_kind", Type: "string", Desc: "deploy failure classification on a failed app: runtime_missing, build_failed, bundle_invalid, readiness_timeout, crashed, server_error, zip_error, transport_error, or unknown"},
		{Name: "attempt_details", Type: "array", Desc: "one entry per failed deploy attempt {attempt int, failure_kind string, error string}; present whenever any attempt failed, including a deploy that succeeded on retry"},
	}, Notes: "Per-app results carry failure_kind (set when status is failed) and attempt_details (failed attempts only) so the reason a deploy attempt failed is visible without reading server logs."},
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
