package config

import (
	"log/slog"
	"os"
	"sort"
	"strings"
)

// knownEnvNames is every SHINYHUB_-prefixed environment variable this codebase
// reads. Configuration is applied from a hand-written allowlist of literal
// lookups, so a name that is not on this list is not read anywhere: setting it
// has no effect at all. TestKnownEnvNamesCoversEverySHINYHUBLookup rebuilds this
// set from the source and fails if the two disagree, so the list cannot drift
// away from the lookups it describes.
var knownEnvNames = []string{
	"SHINYHUB_ADMIN_PASSWORD",
	"SHINYHUB_ADMIN_USER",
	"SHINYHUB_APPS_DIR",
	"SHINYHUB_APP_DATA_DIR",
	"SHINYHUB_APP_ENV_ALLOW",
	"SHINYHUB_APP_LOG_MAX_SIZE_MB",
	"SHINYHUB_APP_LOG_RUN_RETENTION_COUNT",
	"SHINYHUB_APP_ORIGIN",
	"SHINYHUB_APP_QUOTA_MB",
	"SHINYHUB_AUDIT_RETENTION_DAYS",
	"SHINYHUB_AUTH_GROUP_ROLE_MAPPINGS",
	"SHINYHUB_AUTH_LOCAL_LOGIN",
	"SHINYHUB_AUTH_OAUTH_DEFAULT_ROLE",
	"SHINYHUB_AUTH_SECRET",
	"SHINYHUB_AUTH_SECRET_FILE",
	"SHINYHUB_BASE_URL",
	"SHINYHUB_BRANDING_ASSETS_DIR",
	"SHINYHUB_BRANDING_FAVICON",
	"SHINYHUB_BRANDING_LANDING_PAGE",
	"SHINYHUB_BRANDING_LOGO",
	"SHINYHUB_BRANDING_PRIMARY_COLOR",
	"SHINYHUB_BRANDING_ROOT_BEHAVIOR",
	"SHINYHUB_BRANDING_SITE_TITLE",
	"SHINYHUB_BUILD_PYTHON",
	"SHINYHUB_BUILD_PYTHON_INSTALL_MIRROR",
	"SHINYHUB_BUILD_PYTHON_PREFERENCE",
	"SHINYHUB_CONFIG",
	"SHINYHUB_CONFORMANCE",
	"SHINYHUB_CPU_HELPER",
	"SHINYHUB_CPU_HELPER_BUSY",
	"SHINYHUB_CPU_HELPER_PARENT",
	"SHINYHUB_CREDENTIALS",
	"SHINYHUB_DB_DSN",
	"SHINYHUB_DB_PRE_MIGRATION_SNAPSHOT",
	"SHINYHUB_DB_PRE_MIGRATION_SNAPSHOT_RETENTION",
	"SHINYHUB_DEFAULTS_APP_VISIBILITY",
	"SHINYHUB_DEPLOY_TOKEN",
	"SHINYHUB_DEPLOY_TOKEN_APPS",
	"SHINYHUB_DEPLOY_TOKEN_ROLE",
	"SHINYHUB_DEV_STATIC",
	"SHINYHUB_FARGATE_IT_ASSIGN_PUBLIC_IP",
	"SHINYHUB_FARGATE_IT_CLUSTER",
	"SHINYHUB_FARGATE_IT_COMMAND",
	"SHINYHUB_FARGATE_IT_CONTAINER",
	"SHINYHUB_FARGATE_IT_EXPECT_LOG",
	"SHINYHUB_FARGATE_IT_PORT",
	"SHINYHUB_FARGATE_IT_REGION",
	"SHINYHUB_FARGATE_IT_SECURITY_GROUPS",
	"SHINYHUB_FARGATE_IT_SUBNETS",
	"SHINYHUB_FARGATE_IT_TASKDEF",
	"SHINYHUB_FLEET_PTY_CI",
	"SHINYHUB_FLEET_PTY_GITLAB_CI",
	"SHINYHUB_FLEET_PTY_HELPER",
	"SHINYHUB_FORWARD_AUTH_ADMIN_GROUPS",
	"SHINYHUB_FORWARD_AUTH_DEFAULT_ROLE",
	"SHINYHUB_FORWARD_AUTH_EMAIL_HEADER",
	"SHINYHUB_FORWARD_AUTH_ENABLED",
	"SHINYHUB_FORWARD_AUTH_GROUPS_HEADER",
	"SHINYHUB_FORWARD_AUTH_NAME_HEADER",
	"SHINYHUB_FORWARD_AUTH_REQUIRE_GROUPS_HEADER",
	"SHINYHUB_FORWARD_AUTH_SECRET_HEADER",
	"SHINYHUB_FORWARD_AUTH_SHARED_SECRET",
	"SHINYHUB_FORWARD_AUTH_USER_HEADER",
	"SHINYHUB_GITHUB_CALLBACK_URL",
	"SHINYHUB_GITHUB_CLIENT_ID",
	"SHINYHUB_GITHUB_CLIENT_SECRET",
	"SHINYHUB_GOOGLE_CALLBACK_URL",
	"SHINYHUB_GOOGLE_CLIENT_ID",
	"SHINYHUB_GOOGLE_CLIENT_SECRET",
	"SHINYHUB_HOST",
	"SHINYHUB_HOST_CAPACITY_CORES",
	"SHINYHUB_HOST_CAPACITY_MEMORY_MB",
	"SHINYHUB_IDENTITY_HEADERS",
	"SHINYHUB_IDENTITY_STRICT",
	"SHINYHUB_LIVE_UV",
	"SHINYHUB_LL_BASE",
	"SHINYHUB_LOG_FORMAT",
	"SHINYHUB_LOG_LEVEL",
	"SHINYHUB_MAX_BUNDLE_MB",
	"SHINYHUB_METRICS_ADDR",
	"SHINYHUB_METRICS_ENABLED",
	"SHINYHUB_METRICS_HISTORY_INTERVAL",
	"SHINYHUB_METRICS_HISTORY_WINDOW",
	"SHINYHUB_NEW_AUTH_SECRET",
	"SHINYHUB_OIDC_CALLBACK_URL",
	"SHINYHUB_OIDC_CLIENT_ID",
	"SHINYHUB_OIDC_CLIENT_SECRET",
	"SHINYHUB_OIDC_DISPLAY_NAME",
	"SHINYHUB_OIDC_GROUPS_CLAIM",
	"SHINYHUB_OIDC_GROUPS_SCOPE",
	"SHINYHUB_OIDC_ISSUER_URL",
	"SHINYHUB_OIDC_REQUIRE_VALID_GROUPS",
	"SHINYHUB_OPERATOR_AUDIT_ACCESS",
	"SHINYHUB_PID_FILE",
	"SHINYHUB_PROVIDER_LOG_IT_EXPECT",
	"SHINYHUB_PROVIDER_LOG_IT_GROUP",
	"SHINYHUB_PROVIDER_LOG_IT_REGION",
	"SHINYHUB_PROVIDER_LOG_IT_STREAM",
	"SHINYHUB_RUNTIME_AUTOSCALE_COOLDOWN",
	"SHINYHUB_RUNTIME_AUTOSCALE_DEFAULT_TARGET",
	"SHINYHUB_RUNTIME_AUTOSCALE_ENABLED",
	"SHINYHUB_RUNTIME_AUTOSCALE_SCAN_INTERVAL",
	"SHINYHUB_RUNTIME_DEFAULT_MAX_SESSIONS_PER_REPLICA",
	"SHINYHUB_RUNTIME_DEFAULT_REPLICAS",
	"SHINYHUB_RUNTIME_DEFAULT_WORKER_ISOLATION",
	"SHINYHUB_RUNTIME_DOCKER_DEFAULT_CPU_PERCENT",
	"SHINYHUB_RUNTIME_DOCKER_DEFAULT_MEMORY_MB",
	"SHINYHUB_RUNTIME_DOCKER_IMAGE_PYTHON",
	"SHINYHUB_RUNTIME_DOCKER_IMAGE_R",
	"SHINYHUB_RUNTIME_DOCKER_NETWORK_MODE",
	"SHINYHUB_RUNTIME_DOCKER_SOCKET",
	"SHINYHUB_RUNTIME_FARGATE_ASSIGN_PUBLIC_IP",
	"SHINYHUB_RUNTIME_FARGATE_BUNDLE_TOKEN_TTL",
	"SHINYHUB_RUNTIME_FARGATE_CLUSTER",
	"SHINYHUB_RUNTIME_FARGATE_CONTAINER_NAME",
	"SHINYHUB_RUNTIME_FARGATE_CONTROL_PLANE_URL",
	"SHINYHUB_RUNTIME_FARGATE_DEFAULT_CPU_PERCENT",
	"SHINYHUB_RUNTIME_FARGATE_DEFAULT_MEMORY_MB",
	"SHINYHUB_RUNTIME_FARGATE_LAUNCH_TYPE",
	"SHINYHUB_RUNTIME_FARGATE_PLATFORM_VERSION",
	"SHINYHUB_RUNTIME_FARGATE_REGION",
	"SHINYHUB_RUNTIME_FARGATE_ROUTE_VIA_PUBLIC_IP",
	"SHINYHUB_RUNTIME_FARGATE_SECRETS_KMS_KEY_ID",
	"SHINYHUB_RUNTIME_FARGATE_SECRETS_NAME_PREFIX",
	"SHINYHUB_RUNTIME_FARGATE_SECURITY_GROUPS",
	"SHINYHUB_RUNTIME_FARGATE_SUBNETS",
	"SHINYHUB_RUNTIME_FARGATE_TASK_CPU_UNITS",
	"SHINYHUB_RUNTIME_FARGATE_TASK_DEFINITION",
	"SHINYHUB_RUNTIME_FARGATE_TASK_MEMORY_MB",
	"SHINYHUB_RUNTIME_MAX_REPLICAS",
	"SHINYHUB_RUNTIME_MODE",
	"SHINYHUB_RUNTIME_NATIVE_ISOLATION",
	"SHINYHUB_RUNTIME_SCALEWAY_BUNDLE_TOKEN_TTL",
	"SHINYHUB_RUNTIME_SCALEWAY_CONTROL_PLANE_URL",
	"SHINYHUB_RUNTIME_SCALEWAY_DEFAULT_MEMORY_MB",
	"SHINYHUB_RUNTIME_SCALEWAY_DEFAULT_MVCPU",
	"SHINYHUB_RUNTIME_SCALEWAY_DURABLE_DATA",
	"SHINYHUB_RUNTIME_SCALEWAY_IMAGE",
	"SHINYHUB_RUNTIME_SCALEWAY_NAMESPACE_ID",
	"SHINYHUB_RUNTIME_SCALEWAY_NAME_PREFIX",
	"SHINYHUB_RUNTIME_SCALEWAY_PRIVATE_NETWORK_ID",
	"SHINYHUB_RUNTIME_SCALEWAY_PROJECT_ID",
	"SHINYHUB_RUNTIME_SCALEWAY_REGION",
	"SHINYHUB_RUNTIME_SNAPSHOT_ENABLED",
	"SHINYHUB_RUNTIME_SNAPSHOT_MAX_SUSPENDED",
	"SHINYHUB_RUNTIME_SNAPSHOT_RECLAIM_MIN_FRACTION",
	"SHINYHUB_SCHEDULER_TIMEZONE",
	"SHINYHUB_SCHEDULE_RUN_RETENTION_COUNT",
	"SHINYHUB_SERVER_APP_NAV",
	"SHINYHUB_SERVER_HOST",
	"SHINYHUB_SERVER_HOST_BUDGET_MB",
	"SHINYHUB_SERVER_MIN_AVAILABLE_MEMORY_MB",
	"SHINYHUB_SERVER_PORT",
	"SHINYHUB_SERVER_STATUS_OVERLAY",
	"SHINYHUB_SESSION_RECHECK_INTERVAL",
	"SHINYHUB_SHUTDOWN_APPS",
	"SHINYHUB_STOP_GRACE",
	"SHINYHUB_STORAGE_VERSION_RETENTION",
	"SHINYHUB_SUPPORT_SESSIONS",
	"SHINYHUB_TARGET_DSN",
	"SHINYHUB_TEST_CI_CHILD",
	"SHINYHUB_TEST_POSTGRES_DSN",
	"SHINYHUB_TEST_SCALEWAY_LONG",
	"SHINYHUB_TOKEN",
	"SHINYHUB_TRACING_AUTO_INSTRUMENT_APPS",
	"SHINYHUB_TRACING_ENABLED",
	"SHINYHUB_TRACING_OTLP_ENDPOINT",
	"SHINYHUB_TRACING_OTLP_HEADERS",
	"SHINYHUB_TRACING_OTLP_PROTOCOL",
	"SHINYHUB_TRACING_RING_BUFFER_SIZE",
	"SHINYHUB_TRACING_SAMPLE_RATIO",
	"SHINYHUB_TRACING_SLOW_REQUEST_MS",
	"SHINYHUB_TRACING_TRACE_LINK_TEMPLATE",
	"SHINYHUB_TRUSTED_PROXIES",
	"SHINYHUB_UPGRADE_TIMEOUT",
	"SHINYHUB_USAGE_AGGREGATE_RETENTION_DAYS",
	"SHINYHUB_USAGE_ENABLED",
	"SHINYHUB_USAGE_IDENTITY_MODE",
	"SHINYHUB_USAGE_RAW_RETENTION_DAYS",
	"SHINYHUB_WORKER_ADVERTISE_HOSTS",
	"SHINYHUB_WORKER_CA",
	"SHINYHUB_WORKER_CA_DIR",
	"SHINYHUB_WORKER_ENABLED",
	"SHINYHUB_WORKER_JOIN_TOKEN_FILE",
	"SHINYHUB_WORKER_LISTEN_ADDR",
}

var knownEnvSet = func() map[string]bool {
	m := make(map[string]bool, len(knownEnvNames))
	for _, n := range knownEnvNames {
		m[n] = true
	}
	return m
}()

// UnrecognizedEnvName reports one unrecognized SHINYHUB_ variable: the name as
// set, and the known names it plausibly meant. Suggest is usually one name and
// is empty when nothing is close; it lists several rather than choosing between
// equally good candidates, because a confident wrong correction costs more than
// a short list.
type UnrecognizedEnvName struct {
	Name    string
	Suggest []string
}

// UnrecognizedEnvNames returns the SHINYHUB_ variables in environ that nothing
// reads. environ is in os.Environ form ("NAME=value").
//
// A misspelt name is otherwise completely silent: the server boots on defaults
// and the operator sees a healthy log on the wrong port, with nothing anywhere
// naming the variable they set. SHINYHUB_PORT for SHINYHUB_SERVER_PORT is the
// natural guess and produces exactly that.
func UnrecognizedEnvNames(environ []string) []UnrecognizedEnvName {
	var out []UnrecognizedEnvName
	for _, entry := range environ {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(name, "SHINYHUB_") || knownEnvSet[name] {
			continue
		}
		out = append(out, UnrecognizedEnvName{Name: name, Suggest: nearestEnvNames(name)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// warnUnrecognizedEnv logs one warning per unrecognized name. It warns rather
// than fails: an operator may legitimately carry an unrelated SHINYHUB_
// variable in a shared environment, and refusing to boot over it would be
// worse than the silence it replaces.
func warnUnrecognizedEnv() {
	for _, u := range UnrecognizedEnvNames(os.Environ()) {
		if len(u.Suggest) > 0 {
			slog.Warn("ignoring unrecognized environment variable; nothing reads it",
				"name", u.Name, "did_you_mean", strings.Join(u.Suggest, ", "))
			continue
		}
		slog.Warn("ignoring unrecognized environment variable; nothing reads it", "name", u.Name)
	}
}

// nearestEnvNames returns the known names name plausibly meant, or nil when
// nothing is near enough to be worth printing.
//
// Two rules, in order. A name whose every underscore-separated word appears in
// a known name is a dropped-word guess rather than a typo, and the best match
// is the known name that adds the fewest words: SHINYHUB_PORT is one word from
// SHINYHUB_SERVER_PORT and two from SHINYHUB_FARGATE_IT_PORT. Edit distance
// cannot express that - it prefers whichever name is shortest, which is how
// SHINYHUB_PORT ends up "meaning" SHINYHUB_HOST. Everything else is a
// misspelling, and there edit distance is exactly right.
func nearestEnvNames(name string) []string {
	words := map[string]bool{}
	for _, w := range strings.Split(name, "_") {
		words[w] = true
	}

	var best []string
	bestScore := 0
	for _, known := range knownEnvNames {
		knownWords := strings.Split(known, "_")
		covered := 0
		for _, w := range knownWords {
			if words[w] {
				covered++
			}
		}
		if covered < len(words) {
			continue
		}
		// Fewer added words is a closer fit.
		extra := len(knownWords) - len(words)
		if best == nil || extra < bestScore {
			best, bestScore = []string{known}, extra
			continue
		}
		if extra == bestScore {
			best = append(best, known)
		}
	}
	if best != nil {
		return best
	}

	// The bound scales with length so a short name does not match half the
	// list, and a suggestion is never more than a third of the name away.
	limit := len(name) / 3
	if limit < 2 {
		limit = 2
	}
	bestScore = limit + 1
	for _, known := range knownEnvNames {
		d := editDistance(name, known)
		if d > limit {
			continue
		}
		if d < bestScore {
			best, bestScore = []string{known}, d
			continue
		}
		if d == bestScore {
			best = append(best, known)
		}
	}
	return best
}

// editDistance is Levenshtein distance, used only to rank candidate names.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
