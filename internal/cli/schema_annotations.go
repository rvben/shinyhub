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
		ArgTypes: map[string]string{"--data-dir": "path", "--env-file": "path", "--state-dir": "path"},
		OutputFields: []fieldSpec{
			{Name: "slug", Type: "string"},
			{Name: "url", Type: "string"},
			{Name: "port", Type: "integer"},
			{Name: "status", Type: "string"},
		}},

	// ── server-side commands ─────────────────────────────────────────────────
	"init": {Mutating: mut, ArgTypes: map[string]string{"--config": "path", "--admin-password-file": "path"}, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "initialized"},
		{Name: "config_path", Type: "string"},
		{Name: "database_dsn", Type: "string"},
		{Name: "url", Type: "string"},
		{Name: "username", Type: "string"},
		{Name: "created_config", Type: "boolean"},
		{Name: "created_admin", Type: "boolean"},
	}},
	"serve": {Mutating: mut, Streaming: true},
	"backup": {Mutating: mut, ArgTypes: map[string]string{"--out": "path"}, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "written"},
		{Name: "path", Type: "string"},
	}},
	"restore": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "restored"},
		{Name: "archive", Type: "string"},
	}},
	"rotate-secret": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "rotated"},
		{Name: "env_secrets", Type: "integer", Desc: "number of app-env secrets re-encrypted"},
		{Name: "worker_ca_rotated", Type: "boolean", Desc: "whether the worker CA key was re-encrypted"},
	}},
	"migrate-backend": {Mutating: mut, ArgTypes: map[string]string{"--to": "string"}, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "migrated"},
		{Name: "tables", Type: "integer", Desc: "number of tables copied"},
		{Name: "rows", Type: "integer", Desc: "total rows copied"},
	}},
	"worker": {Mutating: mut, Streaming: true},

	// ── auth ─────────────────────────────────────────────────────────────────
	"connect": {Mutating: mut, ArgTypes: map[string]string{"--token-file": "path"}, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "connected, current, or refreshed"},
		{Name: "host", Type: "string", Desc: "Normalized server URL the credential was saved under"},
		{Name: "name", Type: "string", Desc: "Short alias for the server; empty when unset"},
		{Name: "user", Type: "string", Desc: "Authenticated username"},
		{Name: "role", Type: "string", Desc: "Server role for the authenticated user"},
		{Name: "can_create_apps", Type: "boolean", Desc: "Whether this identity may deploy a new app"},
		{Name: "cli_version", Type: "string", Desc: "Version of the local CLI"},
		{Name: "server_version", Type: "string", Desc: "Version reported by the remote server"},
		{Name: "protocol_version", Type: "integer", Desc: "API protocol version reported by the remote server; zero for a legacy server"},
		{Name: "compatibility", Type: "string", Desc: "compatible or warning; incompatible servers are rejected before credentials are used"},
		{Name: "runtimes", Type: "array", Desc: "App runtimes the server reports as available"},
		{Name: "credentials_path", Type: "string", Desc: "Private file holding saved credentials"},
		{Name: "switched_from", Type: "string", Desc: "Previously-current server; empty when unchanged"},
		{Name: "credential", Type: "object", Desc: "Safe lifecycle metadata for the authenticated credential"},
		{Name: "previous_credential_revoked", Type: "boolean", Desc: "With --refresh, whether the previous server-side API key was revoked automatically"},
		{Name: "revoke_warning", Type: "string", Desc: "With --refresh, manual revocation guidance when automatic revocation failed"},
	}},
	"doctor": {Mutating: ro, ArgTypes: map[string]string{"dir": "path"}, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "ready or not_ready"},
		{Name: "scope", Type: "string", Desc: "all, local, or remote"},
		{Name: "app_dir", Type: "string", Desc: "Absolute app directory when local checks run"},
		{Name: "host", Type: "string", Desc: "Selected remote server URL when remote checks run"},
		{Name: "slug", Type: "string", Desc: "Deployment target checked for access"},
		{Name: "checks", Type: "array", Desc: "Ordered checks with name, status, detail, optional fix, and structured credential metadata for credential-lifecycle"},
		{Name: "summary", Type: "object", Desc: "Counts of passed, warned, failed, and skipped checks"},
		{Name: "next_steps", Type: "array", Desc: "Exact commands or actions to continue"},
	}, Notes: "Read-only aggregate preflight. Exits 0 when ready (warnings do not fail), 1 for local/configuration blockers, 3 for auth/network blockers, and 6 when the selected host is reachable but ShinyHub is not ready."},
	"login": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "added when the server was not saved before, refreshed when its credential was replaced"},
		{Name: "host", Type: "string", Desc: "Normalized server URL the credential was saved under"},
		{Name: "name", Type: "string", Desc: "Short alias for this server, usable as `shinyhub use <name>`; empty when unset"},
		{Name: "user", Type: "string", Desc: "Username the saved credential authenticates as; empty when the server did not report one"},
		{Name: "current", Type: "boolean", Desc: "Always true: logging in makes that server current"},
		{Name: "switched_from", Type: "string", Desc: "Server that was current before this login; empty when it did not change"},
		{Name: "credentials_path", Type: "string", Desc: "File the credential was written to"},
	}},
	"logout": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "logged_out"},
		{Name: "host", Type: "string", Desc: "Server signed out of; absent with --all, which reports hosts instead"},
		{Name: "hosts", Type: "array", Desc: "Servers signed out of; present only with --all"},
		{Name: "current_host", Type: "string", Desc: "Server now current; empty when none are left"},
		{Name: "remaining_hosts", Type: "number", Desc: "Count of servers still saved"},
		{Name: "credentials_removed", Type: "boolean", Desc: "Whether the credentials file itself was deleted (true only when no servers remain)"},
	}},
	"whoami": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "ok"},
		{Name: "username", Type: "string"},
		{Name: "role", Type: "string"},
		{Name: "host", Type: "string", Desc: "Server URL the credentials target"},
		{Name: "can_create_apps", Type: "boolean"},
		{Name: "credential", Type: "object", Desc: "Credential type, name, creation/last-use/expiry timestamps, lifecycle status, and seconds remaining"},
	}},
	"hosts": {Mutating: ro,
		OutputFields: []fieldSpec{
			{Name: "host", Type: "string", Desc: "Normalized server URL"},
			{Name: "name", Type: "string", Desc: "Short alias, usable as `shinyhub use <name>`; empty when unset"},
			{Name: "user", Type: "string", Desc: "Username the saved credential authenticates as; empty when not recorded"},
			{Name: "current", Type: "boolean", Desc: "Whether commands target this server by default"},
			{Name: "saved_at", Type: "string", Desc: "RFC 3339 timestamp of the last login to this server"},
		},
		EnvelopeFields: []fieldSpec{
			{Name: "items", Type: "array"},
			{Name: "total", Type: "number"},
			{Name: "limit", Type: "number"},
			{Name: "offset", Type: "number"},
			{Name: "current_host", Type: "string", Desc: "Server commands target by default; empty when none is selected"},
		},
		Notes: "Reads the local credentials file only; contacts no server, so it still answers when every saved server is down. Tokens are never printed.",
	},
	"use": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "switched, or unchanged when that server was already current"},
		{Name: "host", Type: "string", Desc: "Normalized server URL now current"},
		{Name: "name", Type: "string", Desc: "Short alias for that server; empty when unset"},
		{Name: "user", Type: "string", Desc: "Username the saved credential authenticates as; empty when not recorded"},
	}},

	// ── plan / deploy ────────────────────────────────────────────────────────
	"plan": {
		Mutating: ro,
		ArgTypes: map[string]string{"dir": "path"},
		ArgEnums: map[string][]string{
			"--visibility": {"private", "shared", "public", "internal"},
		},
		OutputFields: []fieldSpec{
			{Name: "status", Type: "string", Desc: "planned"},
			{Name: "action", Type: "string", Desc: "create, update, or redeploy"},
			{Name: "change_status", Type: "string", Desc: "new, changed, unchanged, or unknown"},
			{Name: "changes", Type: "boolean|null", Desc: "Null when the remote server has no comparable live digest"},
			{Name: "host", Type: "string"},
			{Name: "app_url", Type: "string"},
			{Name: "slug", Type: "string"},
			{Name: "source", Type: "string"},
			{Name: "permission", Type: "string"},
			{Name: "visibility", Type: "string"},
			{Name: "lifecycle", Type: "string"},
			{Name: "remote", Type: "object"},
			{Name: "bundle", Type: "object", Desc: "Exact digest, sizes, files, and excluded paths"},
			{Name: "launch", Type: "object"},
			{Name: "manifest", Type: "object"},
			{Name: "warnings", Type: "array"},
			{Name: "deploy_command", Type: "string", Desc: "Copy-pasteable command that executes the planned deployment"},
			{Name: "exit_code", Type: "integer", Desc: "Process exit code for this plan invocation"},
		},
		Notes: "Read-only: builds the local archive and makes only GET requests. --detailed-exitcode and --fail-on-changes exit 2 for new, changed, or unknown content; unchanged content exits 0.",
	},
	"plan show": {Mutating: ro, ArgTypes: map[string]string{"PLAN": "path"}},
	"apply":     {Mutating: mut, ArgTypes: map[string]string{"PLAN": "path"}},
	"deploy": {
		Mutating: mut, Streaming: true,
		ArgEnums: map[string][]string{
			"--visibility": {"private", "shared", "public", "internal"},
		},
		OutputFields: []fieldSpec{
			{Name: "status", Type: "string", Desc: "deployed"},
			{Name: "slug", Type: "string"},
			{Name: "deploy_count", Type: "integer", Desc: "Cumulative deployment number; 0 when the server does not report one"},
			{Name: "version", Type: "string", Desc: "Version string from the deployment; empty when the server does not report one"},
			{Name: "kept_stopped", Type: "boolean", Desc: "True when the app was stopped before this deploy and was left stopped; start it with 'apps start <slug>' or deploy with --start"},
			{Name: "url", Type: "string", Desc: "Canonical user-facing app URL"},
			{Name: "opened", Type: "boolean", Desc: "True when --open successfully launched the default browser; false otherwise"},
			{Name: "warm_restarted", Type: "boolean", Desc: "True when --restart-after-warm cycled serving replicas after every first-fire succeeded"},
			{Name: "hooks_declared", Type: "integer", Desc: "Declared post-deploy hooks; omitted when zero or when an older server does not report hook counts"},
			{Name: "hooks_run", Type: "integer", Desc: "Post-deploy hooks completed successfully; omitted when zero"},
			{Name: "hooks_skipped", Type: "integer", Desc: "Post-deploy hooks skipped by a non-host runtime; omitted when zero"},
		},
		Notes: "--open implies --start and --wait, verifies public apps through their user-facing route, and launches the default browser; browser-launch failure is non-fatal and leaves opened=false with a copyable URL. --restart-after-warm implies --wait-for-warm and cycles serving replicas only after every run_on_register first-fire succeeds; deliberately stopped apps remain stopped. --output json emits one final result document. --output ndjson emits ordered phase records followed by exactly one result or error event; event types are phase, result, and error, and phase statuses are started, progress, completed, warning, and failed. Older servers fall back to a synthetic terminal event. shinyhub.toml [app] startup_timeout_seconds (1-3600, default 120) sets the readiness deadline the deploy health check allows before declaring the app crashed; readiness_path (absolute path, default /) selects the GET endpoint and readiness_status (100-599, omitted accepts 2xx/3xx) can require an exact response. These travel with the bundle and also apply on local run, wake, scale, and rollback. build_timeout_seconds (30-7200, default 900) bounds the host-side environment build (uv sync / renv restore) that runs before readiness; a build that exceeds it fails build_failed, distinct from startup_timeout_seconds, and is inert under the docker runtime. shinyhub.toml [app] also accepts memory_limit_mb (0 or 16-1048576) and cpu_quota_percent (0 or 1-6400; 100 = 1 core) - per-replica cgroup v2 ceilings reconciled into the app on deploy (declared-only, like replicas); 0 = explicit unlimited, omitted = unchanged. Clear back to inherit-global with `shinyhub apps set --memory-limit-mb -1` / `--cpu-quota-percent -1`.",
	},

	// ── apps (container) ─────────────────────────────────────────────────────
	"apps": {Mutating: ro},

	"apps list": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "slug", Type: "string"},
		{Name: "status", Type: "string", Desc: "Observed state, including running | idle | stopped | hibernated | degraded | crashed"},
		{Name: "desired_status", Type: "string", Desc: "Persisted lifecycle intent when it differs from observed status"},
		{Name: "replicas", Type: "integer", Desc: "Replica processes configured"},
		{Name: "replicas_running", Type: "integer", Desc: "Replica processes currently running"},
		{Name: "workers_running", Type: "integer", Desc: "Elastic workers currently running"},
		{Name: "effective_hibernate_timeout_minutes", Type: "number", Desc: "Per-app timeout resolved against the server default"},
		{Name: "sessions_ceiling", Type: "integer", Desc: "Configured admission capacity; 0 when uncapped"},
		{Name: "last_replica_error", Type: "string", Desc: "Most recent live replica failure, even while restart is pending"},
		{Name: "deploy_count", Type: "integer"},
		{Name: "deploying", Type: "boolean", Desc: "true only while a deployment or rollback is actively executing"},
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
		{Name: "desired_status", Type: "string", Desc: "Persisted lifecycle intent when status is overlaid from live process reality"},
		{Name: "access", Type: "string"},
		{Name: "owner_id", Type: "integer"},
		{Name: "replicas", Type: "integer"},
		{Name: "replicas_running", Type: "integer", Desc: "Replica processes currently running"},
		{Name: "workers_running", Type: "integer", Desc: "Elastic workers currently running"},
		{Name: "effective_hibernate_timeout_minutes", Type: "number", Desc: "Per-app timeout resolved against the server default"},
		{Name: "sessions_ceiling", Type: "integer", Desc: "Configured admission capacity; 0 when uncapped"},
		{Name: "last_replica_error", Type: "string", Desc: "Most recent live replica failure, even while restart is pending"},
		{Name: "max_sessions_per_replica", Type: "integer"},
		{Name: "memory_limit_mb", Type: "integer", Desc: "Per-replica memory ceiling in MiB; null = inherit global default, 0 = unlimited"},
		{Name: "cpu_quota_percent", Type: "integer", Desc: "Per-replica CPU ceiling in percent of one core (100 = 1 core); null = inherit, 0 = unlimited"},
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
		{Name: "runtime_mode", Type: "string", Desc: "native | docker"},
		{Name: "resource_enforcement", Type: "object", Desc: "{memory,cpu} booleans: whether each per-app limit is actually enforced (native is best-effort, gated on cgroup delegation)"},
		{Name: "worker_isolation", Type: "string", Desc: "Session isolation mode as stored: multiplex | grouped | per_session, or empty when the app inherits the fleet default"},
		{Name: "effective_worker_isolation", Type: "string", Desc: "worker_isolation resolved against the fleet default, so never empty; read this to decide what the app's pool supports"},
		{Name: "worker_grouped_size", Type: "integer", Desc: "Clients per grouped worker (>= 1 when isolation is grouped)"},
		{Name: "worker_max_workers", Type: "integer", Desc: "Demand-driven worker ceiling for grouped/per_session modes"},
		{Name: "worker_warm_spares", Type: "integer", Desc: "Never-used elastic workers kept prebooted; frozen and memory-reclaimed when snapshotting is available"},
		{Name: "worker_max_session_lifetime_secs", Type: "integer", Desc: "Absolute worker lifetime in seconds (0 = unlimited)"},
		{Name: "worker_pool", Type: "object", Desc: "Live elastic capacity view: {mode,sessions_per_worker,max_workers,ceiling,workers:[{slot_id,status,sessions,pid,port}]}; present only while a grouped/per_session pool exists"},
		{Name: "render_seconds", Type: "number", Desc: "Cost of one page render in CPU-seconds; 0 disables render-aware admission pacing"},
		{Name: "render_pacing", Type: "object", Desc: "Advisory pacing summary {render_seconds,effective_cores,cores_source,suggested_max_sessions_per_replica,current_effective_max_sessions_per_replica,cadence_assumption_seconds}; present only while render_seconds > 0"},
		{Name: "rejects_by_reason", Type: "object", Desc: "Rolling 10-minute rejection rollup {window_seconds,counts:{reason:count}}; omitted when the app has had no rejections in the window"},
	}},
	"apps open": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "ready"},
		{Name: "slug", Type: "string"},
		{Name: "url", Type: "string", Desc: "Canonical user-facing app URL"},
		{Name: "opened", Type: "boolean", Desc: "True when the default browser launched; false with --no-browser or when no browser is available"},
		{Name: "app_status", Type: "string", Desc: "App lifecycle status observed before opening"},
	}, Notes: "May wake a sleeping app through its user-facing route. Public running routes are smoke-tested first; private/shared routes use the browser's normal sign-in session and never receive the CLI token."},
	"apps logs": {Mutating: ro, Streaming: true},
	"apps metrics": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "status", Type: "string"},
		{Name: "sessions_cap", Type: "integer", Desc: "Per-replica session cap; for grouped/per_session apps the per-worker cap (ceiling = max_workers x sessions_cap)"},
		{Name: "worker_isolation", Type: "string", Desc: "grouped | per_session when the app runs an elastic pool; omitted for multiplex"},
		{Name: "max_workers", Type: "integer", Desc: "Elastic worker ceiling; omitted for multiplex"},
		{Name: "metrics_available", Type: "boolean"},
		{Name: "cpu_percent", Type: "number", Desc: "Aggregate CPU% across replicas"},
		{Name: "rss_bytes", Type: "integer", Desc: "Aggregate resident memory across replicas"},
		{Name: "replicas", Type: "array", Desc: "Per-replica index, status, pid, cpu_percent, rss_bytes, sessions (bound clients for grouped/per_session workers)"},
	}},
	"apps deployments": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "id", Type: "integer"},
		{Name: "version", Type: "string"},
		{Name: "release_number", Type: "integer", Desc: "Human-friendly v1/v2/… rank among succeeded deploys; null for failed/pending"},
		{Name: "status", Type: "string"},
		{Name: "failure_reason", Type: "string", Desc: "Why a failed deploy failed; empty for pending/succeeded"},
		{Name: "created_at", Type: "string"},
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
	"apps sleep": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "hibernated"},
		{Name: "slug", Type: "string"},
	}},
	"apps delete": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "deleted | absent"},
		{Name: "slug", Type: "string"},
	}},
	"apps set": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "updated"},
		{Name: "slug", Type: "string"},
	}, ArgEnums: map[string][]string{
		"--isolation": {"multiplex", "grouped", "per_session"},
	}, ArgTypes: map[string]string{
		"--render-seconds": "number",
	}},
	"apps transfer": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "transferred"},
		{Name: "slug", Type: "string"},
		{Name: "owner", Type: "string"},
	}},

	// ── apps access (container) ───────────────────────────────────────────────
	"apps access": {Mutating: ro},

	"apps access set": {
		Mutating: mut,
		ArgEnums: map[string][]string{
			"level": {"private", "shared", "public", "internal"},
		},
		OutputFields: []fieldSpec{
			{Name: "status", Type: "string", Desc: "updated"},
			{Name: "slug", Type: "string"},
			{Name: "access", Type: "string"},
		},
		Notes: "shared admits every signed-in user; internal is an accepted alias that the CLI normalizes to shared before sending. private admits only the owner plus granted members/groups. public requires no sign-in.",
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

	// ── top ──────────────────────────────────────────────────────────────────
	"top": {Mutating: ro,
		ArgEnums: map[string][]string{
			"--sort": topSortValues,
		},
		OutputFields: []fieldSpec{
			{Name: "slug", Type: "string"},
			{Name: "status", Type: "string", Desc: "App lifecycle state; deploying while a deploy is in flight"},
			{Name: "replicas_running", Type: "integer", Desc: "Replicas currently running"},
			{Name: "workers_running", Type: "integer", Desc: "Elastic workers currently running"},
			{Name: "replicas_desired", Type: "integer", Desc: "Replicas configured"},
			{Name: "replicas_total", Type: "integer", Desc: "Replicas configured"},
			{Name: "metrics_unavailable", Type: "boolean", Desc: "True when the row came from the app-list fallback after metrics failed"},
			{Name: "cpu_percent", Type: "number", Desc: "CPU summed across running replicas, 100 = one full core; null when none could be measured"},
			{Name: "cpu_percent_partial", Type: "boolean", Desc: "True when a running replica has not reported yet, making cpu_percent a lower bound"},
			{Name: "rss_bytes", Type: "integer", Desc: "Resident memory summed across running replicas; null when none could be measured"},
			{Name: "rss_bytes_partial", Type: "boolean", Desc: "True when a running replica has not reported yet, making rss_bytes a lower bound"},
			{Name: "sessions", Type: "integer", Desc: "Sessions bound to this app's replicas; null when the runtime does not track them"},
			{Name: "sessions_ceiling", Type: "integer", Desc: "Sessions admitted before new ones are refused; 0 when uncapped"},
		},
		EnvelopeFields: []fieldSpec{
			{Name: "items", Type: "array"},
			{Name: "total", Type: "integer"},
			{Name: "limit", Type: "integer"},
			{Name: "offset", Type: "integer"},
			{Name: "host", Type: "string", Desc: "Server the sample was read from"},
			{Name: "captured_at", Type: "string", Desc: "RFC 3339 timestamp of the sample"},
			{Name: "totals", Type: "object", Desc: "Fleet sums: apps, running, cpu_percent, rss_bytes, sessions, and the two *_partial flags"},
		},
		Notes: "Opens an interactive monitor on a terminal: arrows or j/k move, Enter inspects replicas, / filters, Space pauses, Tab cycles sorting, ? shows help, and q exits. Any other output form prints one snapshot and exits, so it is safe in a script. --interval sets the refresh rate (minimum 1s) and applies only to the live form. --fields applies to JSON output; the live table has a fixed layout. A figure the server could not measure is null rather than 0.",
	},

	// ── tokens ───────────────────────────────────────────────────────────────
	"tokens": {Mutating: ro},

	"tokens create": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "id", Type: "integer"},
		{Name: "name", Type: "string"},
		{Name: "token", Type: "string", Desc: "The token value (shown once)"},
		{Name: "created_at", Type: "string"},
		{Name: "expires_at", Type: "string", Desc: "null when the token never expires"},
	}},
	"tokens list": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "id", Type: "integer"},
		{Name: "name", Type: "string"},
		{Name: "created_at", Type: "string"},
		{Name: "expires_at", Type: "string", Desc: "null when the token never expires"},
		{Name: "last_used_at", Type: "string", Desc: "null until the token first authenticates"},
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
	"schedule status": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "slug", Type: "string"},
		{Name: "schedule", Type: "string"},
		{Name: "enabled", Type: "boolean"},
		{Name: "last_run_at", Type: "string", Desc: "RFC3339; null if never run"},
		{Name: "last_run_status", Type: "string"},
		{Name: "last_success_at", Type: "string", Desc: "RFC3339; null if never succeeded"},
		{Name: "last_success_age_s", Type: "integer", Desc: "seconds since last success; null if never succeeded"},
		{Name: "stale", Type: "boolean", Desc: "cron-aware: next expected fire after the last success is overdue"},
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

	// ── projects ──────────────────────────────────────────────────────────────
	"projects": {Mutating: ro},

	"projects list": {Mutating: ro, OutputFields: []fieldSpec{
		{Name: "slug", Type: "string"},
		{Name: "name", Type: "string"},
		{Name: "description", Type: "string"},
		{Name: "icon_emoji", Type: "string"},
		{Name: "app_count", Type: "integer", Desc: "apps in this project that you can see"},
	}, EnvelopeFields: []fieldSpec{
		{Name: "items", Type: "array"},
		{Name: "total", Type: "integer"},
		{Name: "limit", Type: "integer"},
		{Name: "offset", Type: "integer"},
	}},
	"projects set": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "created | updated"},
		{Name: "slug", Type: "string"},
	}},
	"projects rm": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "deleted"},
		{Name: "slug", Type: "string"},
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
	"users revoke-sessions": {Mutating: mut, OutputFields: []fieldSpec{
		{Name: "status", Type: "string", Desc: "revoked"},
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
		{Name: "failure_kind", Type: "string", Desc: "deploy failure classification on a failed app: runtime_missing, build_failed, interpreter_unavailable, hook_failed, bundle_invalid, readiness_timeout, crashed, server_error, zip_error, transport_error, or unknown"},
		{Name: "attempt_details", Type: "array", Desc: "one entry per failed deploy attempt {attempt int, failure_kind string, error string}; present whenever any attempt failed, including a deploy that succeeded on retry"},
		{Name: "warm_restarted", Type: "boolean", Desc: "True when --restart-after-warm cycled this app after its first-fires succeeded"},
	}, Notes: "Per-app results carry failure_kind (set when status is failed) and attempt_details (failed attempts only) so the reason a deploy attempt failed is visible without reading server logs. --restart-after-warm implies --wait-for-warm and cycles serving replicas only after every run_on_register first-fire succeeds. --concurrency (default 3, 1 = serial) bounds how many apps deploy in parallel; lower it on CPU- or memory-constrained hosts since concurrent uv sync / renv restore builds compete for resources."},
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
	"manifest validate": {Mutating: ro, Notes: "Validates shinyhub.toml [app] fields including memory_limit_mb (0 or 16-1048576 MiB) and cpu_quota_percent (0 or 1-6400; 100 = 1 core); out-of-range values are rejected with a clear message."},

	// ── schema ────────────────────────────────────────────────────────────────
	"schema":      {Mutating: ro},
	"healthcheck": {Mutating: ro},
}
