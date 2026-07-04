package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/sandbox"
)

// OAuthConfig holds OAuth2 provider credentials.
type OAuthConfig struct {
	GitHub GitHubOAuthConfig
	Google GoogleOAuthConfig
	OIDC   OIDCConfig
}

// OIDCConfig holds generic OpenID Connect provider credentials and metadata.
type OIDCConfig struct {
	IssuerURL          string
	ClientID           string
	ClientSecret       string
	CallbackURL        string
	DisplayName        string // e.g. "Sign in with Okta"
	GroupsClaim        string // ID-token claim holding group names (default "groups")
	GroupsScope        string // optional extra scope to request (e.g. "groups")
	RequireValidGroups bool   // when true, a malformed groups claim fails the login instead of being skipped
}

// GitHubOAuthConfig holds GitHub OAuth2 application credentials.
type GitHubOAuthConfig struct {
	ClientID     string
	ClientSecret string
	CallbackURL  string
}

// GoogleOAuthConfig holds Google OAuth2 application credentials.
type GoogleOAuthConfig struct {
	ClientID     string
	ClientSecret string
	CallbackURL  string
}

type rawOAuthConfig struct {
	GitHub rawGitHubOAuthConfig `yaml:"github"`
	Google rawGoogleOAuthConfig `yaml:"google"`
	OIDC   rawOIDCConfig        `yaml:"oidc"`
}

type rawOIDCConfig struct {
	IssuerURL          string `yaml:"issuer_url"`
	ClientID           string `yaml:"client_id"`
	ClientSecret       string `yaml:"client_secret"`
	CallbackURL        string `yaml:"callback_url"`
	DisplayName        string `yaml:"display_name"`
	GroupsClaim        string `yaml:"groups_claim"`
	GroupsScope        string `yaml:"groups_scope"`
	RequireValidGroups bool   `yaml:"require_valid_groups"`
}

type rawGitHubOAuthConfig struct {
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	CallbackURL  string `yaml:"callback_url"`
}

type rawGoogleOAuthConfig struct {
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	CallbackURL  string `yaml:"callback_url"`
}

// DefaultsConfig holds default values applied to new resources at creation time.
type DefaultsConfig struct {
	// AppVisibility is the access level assigned to newly created apps when
	// no explicit access is provided in the request. Allowed: "private" (default),
	// "shared", "public".
	AppVisibility string
}

// SchedulerConfig holds scheduler-level settings.
type SchedulerConfig struct {
	// DefaultTimezone is the IANA timezone applied to schedules that do not
	// specify their own timezone. Defaults to "UTC". Set via
	// scheduler.timezone in YAML or SHINYHUB_SCHEDULER_TIMEZONE env var.
	DefaultTimezone string
	// Location is the parsed *time.Location derived from DefaultTimezone. It
	// is populated by Load and is the authoritative value used at runtime.
	Location *time.Location `yaml:"-"`
}

// Config holds all parsed, ready-to-use configuration for ShinyHub.
type Config struct {
	Database         DatabaseConfig
	Server           ServerConfig
	Auth             AuthConfig
	Storage          StorageConfig
	Lifecycle        LifecycleConfig
	Runtime          RuntimeConfig
	Scheduler        SchedulerConfig
	Defaults         DefaultsConfig
	Tracing          TracingConfig
	Metrics          MetricsConfig
	Maintenance      MaintenanceConfig
	Branding         BrandingConfig
	Worker           WorkerConfig
	OAuth            OAuthConfig  `yaml:"-"`
	TrustedProxyNets []*net.IPNet `yaml:"-"` // parsed from Server.TrustedProxies
}

// TracingConfig controls OpenTelemetry trace propagation to app processes and
// the in-memory ring buffer of recent proxy spans surfaced in the UI.
//
// When Enabled is false the entire feature is a no-op: no env vars are injected
// into app processes, the proxy does not propagate traceparent, and the ring
// buffer is empty. When enabled, ShinyHub injects OTEL_* defaults into each app
// process (overridable by user env vars) and retains slow/error proxy spans
// per-app for surfacing in the UI.
type TracingConfig struct {
	Enabled bool
	// OTLPEndpoint is the OTLP receiver URL passed to apps as
	// OTEL_EXPORTER_OTLP_ENDPOINT. Apps export their spans here directly;
	// ShinyHub does not proxy or store them.
	OTLPEndpoint string
	// OTLPProtocol is the wire protocol hint, passed as
	// OTEL_EXPORTER_OTLP_PROTOCOL. Default "http/protobuf" matches the Shiny
	// Python docs. Allowed: "http/protobuf", "grpc".
	OTLPProtocol string
	// OTLPHeaders is an optional comma-separated list of "key=value" pairs
	// passed as OTEL_EXPORTER_OTLP_HEADERS — used by backends like Honeycomb
	// or Grafana Cloud for auth.
	OTLPHeaders string
	// SampleRatio is the head-based sampling probability (0.0–1.0) applied to
	// the proxy's own spans AND propagated to apps via OTEL_TRACES_SAMPLER_ARG.
	// 0 disables sampling (no spans recorded); 1 samples everything.
	SampleRatio float64
	// SlowRequestMS is the latency threshold (in milliseconds) at which a proxy
	// span is retained in the ring buffer regardless of sampling decision. Error
	// spans are always retained. 0 means "retain only error spans".
	SlowRequestMS int
	// RingBufferSize is the maximum number of recent (slow or error) spans
	// retained in-memory per app. Older entries are evicted FIFO. 0 disables
	// the ring buffer entirely.
	RingBufferSize int
	// TraceLinkTemplate is an optional URL template used to deep-link a trace_id
	// to the operator's tracing backend. The substring "{trace_id}" is
	// replaced. Example: "https://grafana.example.com/explore?traceId={trace_id}".
	TraceLinkTemplate string
	// AutoInstrumentApps, when true, launches Python apps under
	// opentelemetry-instrument with the OTEL SDK and transport-layer
	// instrumentors layered into the app's environment via uv's --with
	// overlay. Apps get inbound ASGI and outbound HTTP spans with no bundle
	// changes; the app's own venv and lockfile are never modified. Per-app
	// shinyhub.toml `[tracing] auto` overrides this default in both
	// directions. Requires Enabled; R apps and custom-command apps are
	// never wrapped.
	AutoInstrumentApps bool
}

// MetricsConfig controls the Prometheus scrape endpoint for the ShinyHub server
// process itself (HTTP request counters/latency, Go runtime + process metrics,
// build/version, uptime). It is distinct from the per-app CPU/RAM sampling.
//
// When Enabled is false the feature is a no-op: no /metrics handler and no
// scrape listener are created. When enabled the endpoint is served on its own
// listener at Addr, defaulting to loopback so server internals are never
// exposed on a routable interface by accident; operators who scrape from
// another host set Addr to a private interface behind their own network
// controls (the conventional Prometheus pattern).
type MetricsConfig struct {
	Enabled bool
	// Addr is the listen address for the dedicated metrics listener in
	// "host:port" form. Defaults to "127.0.0.1:9090" when enabled and unset.
	Addr string
	// HistoryWindow is the retention window for the in-memory app-metrics history
	// (CPU/RAM/sessions/instances) shown on the dashboard Trends card. 0 disables
	// collection. Default 12h; a non-zero value must be within [1m, 48h].
	HistoryWindow time.Duration
	// HistoryInterval is the sampling cadence for the history collector.
	// Default 15s; must be within [1s, 10m].
	HistoryInterval time.Duration
}

// MaintenanceConfig controls periodic database housekeeping run on the owner
// instance only. Retention values default to "keep everything" so no history is
// ever deleted unless the operator opts in - the safe default for an audit
// trail and run history.
type MaintenanceConfig struct {
	// AuditRetentionDays deletes audit_events older than this many days. 0 (the
	// default) keeps them forever.
	AuditRetentionDays int `yaml:"audit_retention_days"`
	// ScheduleRunRetentionCount keeps this many newest runs per schedule and
	// deletes older ones. 0 (the default) keeps all runs.
	ScheduleRunRetentionCount int `yaml:"schedule_run_retention_count"`
	// Interval is how often the maintenance loop runs. Defaults to 1h.
	Interval time.Duration `yaml:"interval"`
}

// BrandingConfig customises the ShinyHub front door. Every field is optional;
// the zero value behaves as if no branding is configured.
type BrandingConfig struct {
	SiteTitle   string `yaml:"site_title"`
	AssetsDir   string `yaml:"assets_dir"`
	Logo        string `yaml:"logo"`
	Favicon     string `yaml:"favicon"`
	LandingPage string `yaml:"landing_page"`
	// RootBehavior controls who sees the landing page at GET /:
	//   "" / "auto" - anonymous visitors see the landing page; a signed-in
	//                 ShinyHub user is sent to the SPA home (Overview/Launchpad).
	//   "landing"   - GET / always serves the landing page, even for signed-in
	//                 users (a pure portal). The SPA home stays reachable at /home.
	// Only meaningful when LandingPage is set.
	RootBehavior string       `yaml:"root_behavior"`
	Theme        ThemeConfig  `yaml:"theme"`
	FooterLinks  []FooterLink `yaml:"footer_links"`

	// resolvedAssets maps the public basename served at /branding/<name> to
	// the absolute on-disk path. Populated by Load() after validation.
	resolvedAssets map[string]string `yaml:"-"`
	// landingFile is the absolute path to LandingPage after resolution, or
	// "" when no landing page is configured. Populated by Load().
	landingFile string `yaml:"-"`
}

// ThemeConfig holds theme tokens applied to the stock catalog/login.
type ThemeConfig struct {
	PrimaryColor string `yaml:"primary_color"`
}

// FooterLink is one operator-supplied footer link.
type FooterLink struct {
	Label string `yaml:"label" json:"label"`
	URL   string `yaml:"url" json:"url"`
}

// IsActive reports whether any branding field is set. When false the server
// keeps the existing zero-branding serve path untouched.
func (b BrandingConfig) IsActive() bool {
	return b.SiteTitle != "" || b.AssetsDir != "" || b.Logo != "" ||
		b.Favicon != "" || b.LandingPage != "" || b.Theme.PrimaryColor != "" ||
		len(b.FooterLinks) != 0
}

// LandingFile returns the resolved absolute path of the operator landing
// page, or "" when none is configured.
func (b BrandingConfig) LandingFile() string { return b.landingFile }

// EffectiveRootBehavior normalizes RootBehavior to one of the two supported
// modes, defaulting the empty value to "auto".
func (b BrandingConfig) EffectiveRootBehavior() string {
	if b.RootBehavior == "landing" {
		return "landing"
	}
	return "auto"
}

// ResolvedAssets returns the basename->absolute-path allow-list used by the
// /branding/ asset handler. The returned map is a copy; mutations do not affect
// the config.
func (b BrandingConfig) ResolvedAssets() map[string]string {
	out := make(map[string]string, len(b.resolvedAssets))
	maps.Copy(out, b.resolvedAssets)
	return out
}

// WorkerConfig holds the control-plane settings for hosting remote workers. The
// worker role (shinyhub worker) takes no yaml; it is configured by CLI flags.
type WorkerConfig struct {
	Enabled       bool   `yaml:"enabled"`
	JoinTokenFile string `yaml:"join_token_file"`
	CADir         string `yaml:"ca_dir"`
	// ListenAddr is the TCP address the worker-facing mTLS listener binds to.
	// The effective default when this is empty is 0.0.0.0:8443 (applied by
	// workerListenAddr in cmd/shinyhub/worker.go).
	ListenAddr string `yaml:"listen_addr"`
	// AdvertiseHosts lists the hostnames and IP addresses that remote workers
	// use to reach the control plane. They are placed as SANs in the
	// worker-API server certificate so workers can verify the TLS connection.
	// When empty the certificate defaults to loopback addresses only, which
	// is sufficient for local testing but will fail remote worker connections.
	AdvertiseHosts []string `yaml:"advertise_hosts"`
}

// LifecycleConfig holds parsed lifecycle settings with ready-to-use durations.
type LifecycleConfig struct {
	WatchInterval      time.Duration
	RestartMaxAttempts int
	HibernateTimeout   time.Duration
	// WakeHold is how long the proxy holds a request for a not-yet-routable app
	// while its wake completes, so a warm resume serves inline instead of via the
	// loading page. 0 disables the hold (the loading page is served immediately).
	WakeHold time.Duration
}

type DatabaseConfig struct {
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
}

type ServerConfig struct {
	Host           string   `yaml:"host"`
	Port           int      `yaml:"port"`
	BaseURL        string   `yaml:"base_url"`
	TrustedProxies []string `yaml:"trusted_proxies"`

	// ShutdownApps controls what happens to running app subprocesses /
	// containers when the server receives a shutdown signal:
	//   "adopt" (default) — leave them running; on restart the server
	//                        re-adopts them (zero-downtime upgrades).
	//   "stop"            — gracefully stop every app before exiting
	//                        (clean host state; apps cold-start next boot).
	ShutdownApps string `yaml:"shutdown_apps"`

	// InstanceID uniquely identifies this control-plane process among several
	// running against one database (zero-downtime upgrades / failover). Defaults
	// to "<hostname>-<pid>" when unset.
	InstanceID string `yaml:"instance_id"`

	// LeaseTTL is how long this instance's control-plane ownership lease stays
	// valid without a renewal; LeaseRenewEvery is the renewal cadence. LeaseTTL
	// should be at least 2x LeaseRenewEvery; the elector enforces that floor at
	// startup (config stores the raw values).
	LeaseTTL        time.Duration `yaml:"lease_ttl"`
	LeaseRenewEvery time.Duration `yaml:"lease_renew_every"`

	// DrainTimeout bounds how long a graceful shutdown waits for live WebSocket
	// (hijacked) app sessions to close before force-closing them. Sites with
	// long-lived sessions should raise it. Defaults to 60s.
	DrainTimeout time.Duration `yaml:"drain_timeout"`

	// UpgradeTimeout bounds how long the old process waits for a new one to
	// signal Ready during a zero-downtime upgrade (SIGHUP) before aborting the
	// upgrade and continuing to serve. Defaults to 60s.
	UpgradeTimeout time.Duration `yaml:"upgrade_timeout"`

	// StopGrace is the SIGTERM-to-SIGKILL window when stopping a single app
	// replica (hibernation, stop, restart, shutdown). Raise it for apps that
	// need longer to flush session state on shutdown. Defaults to 10s.
	StopGrace time.Duration `yaml:"stop_grace"`

	// PIDFile, when set, receives the ready process's PID on startup and after
	// each zero-downtime handoff. Required for the systemd path (MAINPID
	// tracking via PIDFile=). Empty (default) writes no PID file.
	PIDFile string `yaml:"pid_file"`

	// HostBudgetMB is the total RAM (in MiB) the host can allocate to app
	// worker processes. Used by the host-capacity guard to reject deploys that
	// would exceed available memory. 0 disables the guard.
	HostBudgetMB int `yaml:"host_budget_mb"`
}

// GroupRoleMapping maps an IdP group name to a global role. Shared by the OIDC
// and forward-auth feeders; mirrored as auth.GroupRoleMapping at the boundary
// (the auth package must not import config).
type GroupRoleMapping struct {
	Group string `yaml:"group"`
	Role  string `yaml:"role"`
}

type AuthConfig struct {
	Secret string `yaml:"secret"`
	// OAuthDefaultRole is the role assigned to users created via just-in-time
	// provisioning during OAuth/OIDC sign-in (i.e. first-time login). Allowed
	// values: "viewer" (default), "developer", "operator". "admin" is
	// intentionally not permitted -- admin must be granted explicitly, never
	// auto-provisioned from an external IdP.
	OAuthDefaultRole string `yaml:"oauth_default_role"`

	// GroupRoleMappings maps IdP groups to global roles. Applied by both OIDC and
	// forward-auth. Highest-rank match wins. admin_groups merges in as role=admin.
	GroupRoleMappings []GroupRoleMapping `yaml:"group_role_mappings"`

	// DeployToken is a pre-shared bearer token sourced from
	// SHINYHUB_DEPLOY_TOKEN. When non-empty it authenticates as the synthetic
	// system user `__deploy__` with role DeployTokenRole. Not persisted; rotation
	// is "change the env var, restart the service."
	DeployToken string `yaml:"-"`

	// DeployTokenRole is the role granted to the synthetic system user when the
	// env-token is active. Sourced from SHINYHUB_DEPLOY_TOKEN_ROLE; default
	// "developer". Must be one of viewer, developer, operator, admin.
	DeployTokenRole string `yaml:"-"`

	ForwardAuth ForwardAuthConfig `yaml:"forward_auth"`

	// IdentityHeaders globally enables forwarding the authenticated user's
	// identity (X-Shinyhub-* headers + signed identity token) to app
	// processes. nil/absent = enabled (the default). Setting false is a hard
	// operator kill switch: per-app manifest opt-ins cannot override it.
	// Per-app `[app] identity_headers = false` opts a single app out.
	IdentityHeaders *bool `yaml:"identity_headers"`

	// LocalLogin enables the built-in username/password login: the sign-in form
	// and the /api/auth/login and /api/auth/session endpoints. nil/absent =
	// enabled (the default). Set false for an SSO-only deployment: the login
	// screen hides the password form AND the password endpoints reject with 403,
	// so a user cannot bypass the IdP by POSTing credentials. Startup fails when
	// this is false and no SSO login path is configured (see HasSSOLoginPath), to
	// avoid locking out every user. Note: this also disables the break-glass
	// admin's password login, so keep at least one SSO admin path.
	LocalLogin *bool `yaml:"local_login"`
}

// IdentityHeadersEnabled reports whether identity headers (X-Shinyhub-* and
// the signed identity token) are globally permitted to be forwarded to app
// processes. Returns true when the field is absent (the default).
func (a *AuthConfig) IdentityHeadersEnabled() bool {
	return a.IdentityHeaders == nil || *a.IdentityHeaders
}

// LocalLoginEnabled reports whether the built-in username/password login is
// permitted. Returns true when the field is absent (the default).
func (a *AuthConfig) LocalLoginEnabled() bool {
	return a.LocalLogin == nil || *a.LocalLogin
}

// ActiveSSOLoginPaths returns the names of the SSO login paths that are
// configured well enough to attempt a login. GitHub/Google require BOTH a
// client_id and a client_secret (a missing secret fails at the token exchange,
// so a client_id alone is not a login path); OIDC requires an issuer_url (its
// discovery is verified at startup); forward-auth counts when enabled. The order
// is stable so it can be logged. See HasSSOLoginPath for the important caveat
// that "configured" is not "verified working".
func (c *Config) ActiveSSOLoginPaths() []string {
	var paths []string
	if c.OAuth.GitHub.ClientID != "" && c.OAuth.GitHub.ClientSecret != "" {
		paths = append(paths, "github")
	}
	if c.OAuth.Google.ClientID != "" && c.OAuth.Google.ClientSecret != "" {
		paths = append(paths, "google")
	}
	if c.OAuth.OIDC.IssuerURL != "" {
		paths = append(paths, "oidc")
	}
	if c.Auth.ForwardAuth.Enabled {
		paths = append(paths, "forward-auth")
	}
	return paths
}

// HasSSOLoginPath reports whether at least one non-password browser login path is
// configured (see ActiveSSOLoginPaths). This is the SSO-only lockout guard's
// check. IMPORTANT: "configured" is not "verified working" - forward-auth still
// depends on trusted_proxies including the edge proxy AND the proxy sending the
// user header, and OAuth requires a reachable callback URL. Only OIDC is verified
// at startup (discovery). Operators must test SSO end to end before disabling
// local login; the boot log names the paths that were counted.
func (c *Config) HasSSOLoginPath() bool {
	return len(c.ActiveSSOLoginPaths()) > 0
}

// ForwardAuthConfig configures trust of an upstream reverse proxy that has
// already authenticated the user. When Enabled is true, the forward-auth
// middleware trusts UserHeader (and optional EmailHeader / GroupsHeader) on
// requests whose direct peer IP is in Config.TrustedProxyNets.
type ForwardAuthConfig struct {
	Enabled    bool   `yaml:"enabled"`
	UserHeader string `yaml:"user_header"`
	// EmailHeader is the proxy header carrying the user's email (e.g. Authelia's
	// Remote-Email). When set, the middleware captures it request-scoped and
	// forwards it to apps as X-Shinyhub-Email and the identity token's email
	// claim. Not persisted (the users table has no email column). Empty disables
	// email capture.
	EmailHeader string `yaml:"email_header"`
	// NameHeader is the proxy header carrying the user's friendly name (e.g.
	// Authelia's Remote-Name). When set, the middleware captures it as the
	// forward-auth user's display name. Empty (default) disables name capture.
	NameHeader   string   `yaml:"name_header"`
	GroupsHeader string   `yaml:"groups_header"`
	AdminGroups  []string `yaml:"admin_groups"`
	DefaultRole  string   `yaml:"default_role"`
	// RequireGroupsHeader, when true and groups_header is configured, causes a
	// forward-auth request that is missing the groups header to be refused (403)
	// instead of being treated as no groups. Default false keeps the revoke
	// behavior. Forces a misconfigured proxy to fail loudly.
	RequireGroupsHeader bool `yaml:"require_groups_header"`
}

type StorageConfig struct {
	AppsDir          string `yaml:"apps_dir"`
	AppDataDir       string `yaml:"app_data_dir"`
	VersionRetention int    `yaml:"version_retention"`
	// AppQuotaMB caps the total on-disk footprint (bundles + extracted
	// versions + persistent data dir, excluding .shinyhub-upload-tmp/) of a
	// single app, in mebibytes. 0 disables the limit.
	AppQuotaMB int `yaml:"app_quota_mb"`
	// MaxBundleMB caps a single deploy bundle's multipart upload size, in
	// mebibytes. Must stay aligned with the UI's DEPLOY_MAX_BYTES (asserted
	// by a test). 0 means "no cap"; default 128 matches the existing UI.
	MaxBundleMB int `yaml:"max_bundle_mb"`
}

// RuntimeConfig controls how app processes are started and isolated.
type RuntimeConfig struct {
	Mode            string // "native" (default) or "docker"
	Docker          DockerRuntimeConfig
	Native          NativeRuntimeConfig
	DefaultReplicas int
	MaxReplicas     int
	// DefaultMaxSessionsPerReplica is the fallback session cap enforced by the
	// proxy when an app's own max_sessions_per_replica is 0. Once every replica
	// reaches this many active connections, new cookie-less requests are shed
	// with 503. 0 here disables the cap entirely (unlimited).
	DefaultMaxSessionsPerReplica int
	// DefaultWorkerIsolation is the fleet default isolation mode applied when an
	// app's worker_isolation is empty. Almost always "multiplex".
	DefaultWorkerIsolation string
	// Tiers is the ordered list of runtime tiers. The first entry is the
	// default tier (used when a replica has no explicit tier). When the config
	// omits tiers, Load synthesizes a single tier named "local" whose runtime
	// equals Mode, so single-node behavior is unchanged.
	Tiers []TierConfig
	// Autoscale holds the global replica-autoscale controller settings. The
	// controller only ever acts on apps that have opted in (per-app
	// autoscale_enabled); these values govern how it behaves for those apps.
	Autoscale AutoscaleConfig
	// Fargate holds the AWS ECS/Fargate runtime settings. They are required when
	// any tier declares runtime "fargate"; otherwise the zero value is unused.
	Fargate FargateRuntimeConfig
	// Snapshot controls warm-wake (freeze + cgroup reclaim), shared by the
	// native and docker runtimes.
	Snapshot SnapshotConfig
}

// NativeRuntimeConfig holds settings for the native (non-container) runtime.
type NativeRuntimeConfig struct {
	// Isolation is the process-isolation dial for native app processes: "off"
	// (default) or "standard" (Landlock filesystem confinement + NO_NEW_PRIVS,
	// Linux-only, best-effort). Validated at load against sandbox.ParseLevel.
	Isolation string
}

// FargateRuntimeConfig holds the AWS ECS/Fargate runtime settings shared by every
// tier whose runtime is "fargate". Each replica on such a tier runs as one
// Fargate task launched from TaskDefinition, with the app command, env, and
// resource limits applied as container overrides. The proxy routes to the task's
// awsvpc private IP, so the control plane must run inside or peered with the
// task's VPC.
type FargateRuntimeConfig struct {
	// Cluster is the ECS cluster short name or full ARN tasks run on.
	Cluster string
	// TaskDefinition is the family, family:revision, or full ARN of the task
	// definition to run. It must declare a container named ContainerName.
	TaskDefinition string
	// ContainerName is the container within TaskDefinition that per-replica
	// command/env/limit overrides target.
	ContainerName string
	// Subnets are the awsvpc subnet IDs tasks attach to (at least one required).
	Subnets []string
	// SecurityGroups are the awsvpc security group IDs applied to each task ENI.
	SecurityGroups []string
	// AssignPublicIP maps to the awsvpc assignPublicIp setting. Set it for tasks
	// in public subnets without a NAT gateway.
	AssignPublicIP bool
	// PlatformVersion pins the Fargate platform version (e.g. "1.4.0"). Empty
	// uses the ECS default.
	PlatformVersion string
	// Region is the AWS region the ECS client targets. Empty falls back to the
	// SDK's default chain (AWS_REGION, profile, instance metadata).
	Region string
	// RouteViaPublicIP routes to each task's public IP instead of its private IP,
	// for a control plane running outside the task VPC (development/testing only).
	// Requires AssignPublicIP. Production runs the control plane in-VPC and routes
	// over private IPs (default false).
	RouteViaPublicIP bool

	// TaskCPUUnits is the ECS task-level CPU allocation in CPU units (1 vCPU =
	// 1024 units). Must be one of the Fargate-supported values: 256, 512, 1024,
	// 2048, 4096, 8192, 16384. Required when any tier uses runtime "fargate".
	TaskCPUUnits int

	// TaskMemoryMB is the ECS task-level memory allocation in MiB. Must satisfy
	// the Fargate CPU/memory matrix (see validateFargate). Required when any
	// tier uses runtime "fargate".
	TaskMemoryMB int

	// DefaultMemoryMB is the per-container memory limit applied when an app has
	// no explicit memory_limit_mb. 0 means no override (the task definition's
	// container limit applies). Mirrors DockerRuntimeConfig.DefaultMemoryMB.
	DefaultMemoryMB int

	// DefaultCPUPercent is the per-container CPU quota applied when an app has
	// no explicit cpu_quota_percent. 0 means no override. Mirrors
	// DockerRuntimeConfig.DefaultCPUPercent.
	DefaultCPUPercent int

	// ControlPlaneURL is the URL tasks use to fetch their bundle from the control
	// plane. It must be reachable from inside the task's VPC (or subnet, for
	// public-IP mode). Required when any tier uses runtime "fargate".
	// When RouteViaPublicIP is true this must use https:// to prevent the
	// bearer bundle token from travelling in plaintext over the public internet.
	ControlPlaneURL string

	// BundleTokenTTL is how long a minted bundle capability token remains valid.
	// Default 10 minutes. Tasks that take longer than this to start will fail
	// to fetch the bundle; increase if your task cold-start (including image
	// pull) regularly exceeds 10 minutes.
	BundleTokenTTL time.Duration

	// DurableData asserts that this Fargate tier has durable, replica-shared
	// app-data storage (S3 Files, or a volume the operator attached to the base
	// task definition, e.g. EFS). It suppresses the durable-data guard, which
	// otherwise blocks deploying a data-using app onto a Fargate tier whose task
	// storage is ephemeral scratch. Default false. buildFargateRuntime treats the
	// tier as durable if DurableData is true OR an S3 Files backend is configured.
	DurableData bool

	// S3Files, when configured, is the managed durable-data backend: an Amazon S3
	// Files file system mounted into every app's task at MountPath, with each
	// app's data isolated to a per-app subdirectory of RootDirectory. When set,
	// the tier is durable and the control plane injects the volume + mount point
	// into each app's per-app task-definition revision.
	S3Files FargateS3FilesConfig

	// SecretsNamePrefix, when non-empty, enables routing apps' secret env vars
	// through AWS Secrets Manager (referenced by ARN from a per-app task-def
	// secrets block) instead of plaintext task overrides, so they never appear
	// in ecs:DescribeTasks. It namespaces the secret store names and per-app
	// task-definition families; make it unique per ShinyHub installation.
	SecretsNamePrefix string

	// SecretsKMSKeyID optionally encrypts the secrets with a customer-managed KMS
	// key (id, ARN, or alias) instead of the default aws/secretsmanager key. Only
	// meaningful when SecretsNamePrefix is set.
	SecretsKMSKeyID string
}

// FargateS3FilesConfig configures the managed Amazon S3 Files durable-data
// backend for a Fargate tier. When FileSystemArn is set, the control plane
// mounts the file system into every app's task and gives each app an isolated
// per-app subdirectory of RootDirectory.
type FargateS3FilesConfig struct {
	// FileSystemArn is the ARN of the S3 Files file system to mount
	// (arn:aws:s3files:<region>:<account>:file-system/fs-...). Setting it enables
	// the backend and makes the tier durable.
	FileSystemArn string

	// RootDirectory is the file-system directory under which each app gets its
	// own subdirectory (RootDirectory/<slug>), isolating apps from each other.
	// Default "/". Ignored when AccessPointArn is set (the access point fixes the
	// root and the operator owns isolation).
	RootDirectory string

	// AccessPointArn optionally pins the mount to an S3 Files access point, which
	// enforces its own root directory and identity. When set, per-app RootDirectory
	// isolation does not apply; the operator is responsible for isolation.
	AccessPointArn string

	// TransitEncryptionPort is the port for encrypted data between the ECS host
	// and the file system. 0 lets ECS choose. Transit encryption is always on.
	TransitEncryptionPort int

	// MountPath is the absolute container path the volume is mounted at. It must
	// equal the app's working directory + "/data" so the {data_dir} placeholder
	// ("data", relative to the app cwd) resolves onto the mount. Default
	// "/app/bundle/data" (the reference runner's bundle working directory).
	MountPath string
}

// Configured reports whether the S3 Files backend is enabled for this tier.
func (c FargateS3FilesConfig) Configured() bool { return c.FileSystemArn != "" }

// AutoscaleConfig holds the global settings for the replica autoscale
// controller. Autoscaling is opt-in per app; with no app opted in these values
// have no effect.
type AutoscaleConfig struct {
	// Enabled is the global kill switch. When false the controller never runs,
	// regardless of any per-app opt-in. Default false.
	Enabled bool
	// ScanInterval is how often the controller evaluates opted-in apps.
	ScanInterval time.Duration
	// Cooldown is the minimum time between successive scale actions on the same
	// app, damping oscillation.
	Cooldown time.Duration
	// DefaultTarget is the target average active sessions per replica as a
	// fraction (0,1] of the per-replica session cap, used when an app's own
	// autoscale_target is 0.
	DefaultTarget float64
}

// TierConfig names a runtime tier and the runtime that backs it.
// Runtime is one of "native" or "docker" in this phase ("remote_docker"
// arrives with the remote provider).
type TierConfig struct {
	Name       string
	Runtime    string
	LaunchType string // "FARGATE" (default) or "EC2"; only meaningful for fargate tiers
}

// TierOrder returns the tier names in declaration order.
func (r RuntimeConfig) TierOrder() []string {
	out := make([]string, len(r.Tiers))
	for i, t := range r.Tiers {
		out[i] = t.Name
	}
	return out
}

// DefaultTierName returns the first declared tier's name (the default tier).
func (r RuntimeConfig) DefaultTierName() string {
	if len(r.Tiers) == 0 {
		return "local"
	}
	return r.Tiers[0].Name
}

// RuntimeForTier returns the runtime mode backing the named tier.
func (r RuntimeConfig) RuntimeForTier(name string) (string, bool) {
	for _, t := range r.Tiers {
		if t.Name == name {
			return t.Runtime, true
		}
	}
	return "", false
}

// DefaultResourcesForTier returns the platform-default memory limit and CPU
// quota for a replica placed on the named tier. For a "fargate" tier it returns
// the Fargate-specific defaults (runtime.fargate.default_memory_mb /
// default_cpu_percent). For any other runtime it returns the Docker defaults
// (runtime.docker.default_memory_mb / default_cpu_percent), preserving
// existing behaviour for native and docker tiers. A zero value for either
// field means "no limit" as documented.
func (r RuntimeConfig) DefaultResourcesForTier(tier string) (memMB, cpuPct int) {
	rt, _ := r.RuntimeForTier(tier)
	if rt == "fargate" {
		return r.Fargate.DefaultMemoryMB, r.Fargate.DefaultCPUPercent
	}
	return r.Docker.DefaultMemoryMB, r.Docker.DefaultCPUPercent
}

// DefaultResourcesForApp returns the platform-default memory limit and CPU
// quota appropriate for the given app's placement.
//
// When the app is placed on exactly one tier (len(PlacementMap) == 1), that
// tier's defaults are used - this ensures an app placed exclusively on a
// fargate tier receives fargate defaults, not the global default tier's
// defaults. When the app has no recorded placement or is spread across
// multiple tiers, DefaultTierName() is used (first declared tier), preserving
// the existing behaviour for no-placement and multi-tier cases. Multi-tier
// apps retain the documented limitation that a single set of defaults cannot
// serve divergent per-tier requirements.
func (r RuntimeConfig) DefaultResourcesForApp(app *db.App) (memMB, cpuPct int) {
	if app == nil {
		return r.DefaultResourcesForTier(r.DefaultTierName())
	}
	tier := r.DefaultTierName()
	if pm := app.PlacementMap(); len(pm) == 1 {
		for t := range pm {
			tier = t
		}
	}
	return r.DefaultResourcesForTier(tier)
}

// DockerRuntimeConfig holds Docker-specific runtime settings.
type DockerRuntimeConfig struct {
	Socket            string
	Images            DockerImages
	DefaultMemoryMB   int // 0 = no limit
	DefaultCPUPercent int // 0 = no limit; 100 = 1 full core
	// NetworkMode controls the Docker network mode applied to app containers.
	// "bridge" (default) puts each app on the default Docker bridge with an
	// explicit 127.0.0.1:port mapping for the proxy — this preserves the
	// "only the proxy can reach the app" boundary that native mode enforces
	// via 127.0.0.1 binding. "host" disables network isolation; the container
	// shares the host network stack. Allowed: "bridge" (default), "host".
	NetworkMode string
}

// SnapshotConfig controls warm-wake, shared by the native and docker runtimes:
// on hibernate a replica is frozen (SIGSTOP for native, docker pause for docker)
// and its RAM reclaimed to swap via cgroup v2 memory.reclaim instead of being
// stopped, so wake resumes it warm. Disabled by default; when off, apps
// hibernate via Stop exactly as before.
type SnapshotConfig struct {
	Enabled bool
	// MaxSuspended caps concurrently suspended replicas (GC evicts the oldest
	// beyond it). Defaults to 16; a value <= 0 falls back to the default.
	MaxSuspended int
	// ReclaimMinFraction is the minimum fraction of a replica's pre-suspend RSS
	// that must be reclaimed for the freeze to count as "freed"; below it the
	// replica falls back to Stop. Must be in (0, 1]; defaults to 0.8.
	ReclaimMinFraction float64
	// RestoreOnStartup re-boots and re-freezes apps that were hibernated before a
	// server restart, so their next access is a warm resume instead of a cold
	// boot (a frozen process does not survive a service restart). Defaults to
	// true when warm-wake is enabled. No effect when Enabled is false.
	RestoreOnStartup bool
}

// DockerImages holds the base image names for each app type.
type DockerImages struct {
	Python string
	R      string
}

type rawDefaultsConfig struct {
	AppVisibility string `yaml:"app_visibility"`
}

type rawSchedulerConfig struct {
	Timezone string `yaml:"timezone"`
}

// rawConfig mirrors Config for YAML decoding, using string-typed duration fields.
type rawConfig struct {
	Database    DatabaseConfig     `yaml:"database"`
	Server      ServerConfig       `yaml:"server"`
	Auth        AuthConfig         `yaml:"auth"`
	Storage     StorageConfig      `yaml:"storage"`
	Lifecycle   rawLifecycleConfig `yaml:"lifecycle"`
	OAuth       rawOAuthConfig     `yaml:"oauth"`
	Runtime     rawRuntimeConfig   `yaml:"runtime"`
	Scheduler   rawSchedulerConfig `yaml:"scheduler"`
	Defaults    rawDefaultsConfig  `yaml:"defaults"`
	Tracing     rawTracingConfig   `yaml:"tracing"`
	Metrics     rawMetricsConfig   `yaml:"metrics"`
	Maintenance MaintenanceConfig  `yaml:"maintenance"`
	Branding    BrandingConfig     `yaml:"branding"`
	Worker      WorkerConfig       `yaml:"worker"`
}

// validTopLevelKeys is the set of accepted top-level YAML keys, mirroring the
// rawConfig fields' yaml tags. TestValidTopLevelKeys_MatchRawConfig keeps the
// two in sync, so adding a section to rawConfig without listing it here (which
// would make configs using it error) is caught at test time.
var validTopLevelKeys = map[string]bool{
	"database": true, "server": true, "auth": true, "storage": true,
	"lifecycle": true, "oauth": true, "runtime": true, "scheduler": true,
	"defaults": true, "tracing": true, "metrics": true, "maintenance": true,
	"branding": true, "worker": true,
}

// runtimeSubKeys are the section names nested under `runtime:`. Placing one at
// the top level is a common mistake; the unknown-key error hints at the correct
// nesting for these.
var runtimeSubKeys = map[string]bool{
	"docker": true, "tiers": true, "autoscale": true, "fargate": true, "snapshot": true, "native": true,
}

// checkTopLevelKeys rejects any top-level YAML key that is not a known config
// section, so a misplaced or misspelled key is a clear error rather than a
// silent no-op. data is the raw config bytes (already known to decode as a
// mapping, since the struct decode above succeeded).
func checkTopLevelKeys(data []byte) error {
	var top map[string]yaml.Node
	if err := yaml.Unmarshal(data, &top); err != nil {
		// A non-mapping document is already rejected by the struct decode.
		return nil
	}
	var unknown []string
	for k := range top {
		if !validTopLevelKeys[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	slices.Sort(unknown)
	parts := make([]string, 0, len(unknown))
	for _, k := range unknown {
		if runtimeSubKeys[k] {
			parts = append(parts, fmt.Sprintf("%q (did you mean runtime.%s?)", k, k))
		} else {
			parts = append(parts, fmt.Sprintf("%q", k))
		}
	}
	valid := make([]string, 0, len(validTopLevelKeys))
	for k := range validTopLevelKeys {
		valid = append(valid, k)
	}
	slices.Sort(valid)
	return fmt.Errorf("config: unknown top-level key(s): %s; valid top-level keys are: %s",
		strings.Join(parts, ", "), strings.Join(valid, ", "))
}

type rawMetricsConfig struct {
	Enabled         bool   `yaml:"enabled"`
	Addr            string `yaml:"addr"`
	HistoryWindow   string `yaml:"history_window"`   // parsed as time.Duration; "0s" disables
	HistoryInterval string `yaml:"history_interval"` // parsed as time.Duration
}

type rawTracingConfig struct {
	Enabled      bool   `yaml:"enabled"`
	OTLPEndpoint string `yaml:"otlp_endpoint"`
	OTLPProtocol string `yaml:"otlp_protocol"`
	OTLPHeaders  string `yaml:"otlp_headers"`
	// Pointers so an explicit 0 (a documented, meaningful value: 0 disables
	// sampling / disables the ring buffer / retains only error spans) is
	// distinguishable from the key being absent (apply the safe default).
	SampleRatio        *float64 `yaml:"sample_ratio"`
	SlowRequestMS      *int     `yaml:"slow_request_ms"`
	RingBufferSize     *int     `yaml:"ring_buffer_size"`
	TraceLinkTemplate  string   `yaml:"trace_link_template"`
	AutoInstrumentApps bool     `yaml:"auto_instrument_apps"`
}

type rawLifecycleConfig struct {
	WatchInterval      string `yaml:"watch_interval"`
	RestartMaxAttempts int    `yaml:"restart_max_attempts"`
	HibernateTimeout   string `yaml:"hibernate_timeout"`
	WakeHold           string `yaml:"wake_hold"`
}

type rawRuntimeConfig struct {
	Mode            string                 `yaml:"mode"`
	Docker          rawDockerRuntimeConfig `yaml:"docker"`
	Native          rawNativeRuntimeConfig `yaml:"native"`
	DefaultReplicas int                    `yaml:"default_replicas"`
	MaxReplicas     int                    `yaml:"max_replicas"`
	// Pointer so an explicit 0 (documented as "unlimited") is
	// distinguishable from the key being absent (apply the safe default).
	DefaultMaxSessionsPerReplica *int                    `yaml:"default_max_sessions_per_replica"`
	DefaultWorkerIsolation       string                  `yaml:"default_worker_isolation"`
	Tiers                        []rawTierConfig         `yaml:"tiers"`
	Autoscale                    rawAutoscaleConfig      `yaml:"autoscale"`
	Fargate                      rawFargateRuntimeConfig `yaml:"fargate"`
	Snapshot                     rawSnapshotConfig       `yaml:"snapshot"`
}

type rawNativeRuntimeConfig struct {
	Isolation string `yaml:"isolation"`
}

type rawFargateRuntimeConfig struct {
	Cluster           string                  `yaml:"cluster"`
	TaskDefinition    string                  `yaml:"task_definition"`
	ContainerName     string                  `yaml:"container_name"`
	Subnets           []string                `yaml:"subnets"`
	SecurityGroups    []string                `yaml:"security_groups"`
	AssignPublicIP    bool                    `yaml:"assign_public_ip"`
	PlatformVersion   string                  `yaml:"platform_version"`
	Region            string                  `yaml:"region"`
	RouteViaPublicIP  bool                    `yaml:"route_via_public_ip"`
	TaskCPUUnits      int                     `yaml:"task_cpu_units"`
	TaskMemoryMB      int                     `yaml:"task_memory_mb"`
	DefaultMemoryMB   int                     `yaml:"default_memory_mb"`
	DefaultCPUPercent int                     `yaml:"default_cpu_percent"`
	ControlPlaneURL   string                  `yaml:"control_plane_url"`
	BundleTokenTTL    string                  `yaml:"bundle_token_ttl"` // parsed as time.Duration
	DurableData       bool                    `yaml:"durable_data"`
	S3Files           rawFargateS3FilesConfig `yaml:"s3files"`
	Secrets           rawFargateSecretsConfig `yaml:"secrets"`
}

type rawFargateS3FilesConfig struct {
	FileSystemArn         string `yaml:"file_system_arn"`
	RootDirectory         string `yaml:"root_directory"`
	AccessPointArn        string `yaml:"access_point_arn"`
	TransitEncryptionPort int    `yaml:"transit_encryption_port"`
	MountPath             string `yaml:"mount_path"`
}

type rawFargateSecretsConfig struct {
	NamePrefix string `yaml:"name_prefix"`
	KMSKeyID   string `yaml:"kms_key_id"`
}

type rawAutoscaleConfig struct {
	Enabled      bool   `yaml:"enabled"`
	ScanInterval string `yaml:"scan_interval"`
	Cooldown     string `yaml:"cooldown"`
	// Pointer so an explicit 0 is distinguishable from the key being absent.
	DefaultTarget *float64 `yaml:"default_target"`
}

type rawTierConfig struct {
	Name       string `yaml:"name"`
	Runtime    string `yaml:"runtime"`
	LaunchType string `yaml:"launch_type"`
}

type rawDockerRuntimeConfig struct {
	Socket            string          `yaml:"socket"`
	Images            rawDockerImages `yaml:"images"`
	DefaultMemoryMB   int             `yaml:"default_memory_mb"`
	DefaultCPUPercent int             `yaml:"default_cpu_percent"`
	NetworkMode       string          `yaml:"network_mode"`
}

type rawSnapshotConfig struct {
	Enabled            bool    `yaml:"enabled"`
	MaxSuspended       int     `yaml:"max_suspended"`
	ReclaimMinFraction float64 `yaml:"reclaim_min_fraction"`
	// Pointer so an unset value defaults to true while an explicit `false` can
	// turn the startup warm-restore off.
	RestoreOnStartup *bool `yaml:"restore_on_startup"`
}

type rawDockerImages struct {
	Python string `yaml:"python"`
	R      string `yaml:"r"`
}

// Load parses and validates the full server configuration from path (or
// environment variables when path is empty or the file does not exist).
// auth.secret is required and must not be the placeholder value or shorter
// than 32 characters. Use LoadForMaintenance for commands that do not perform
// cryptography (backup, restore).
func Load(path string) (*Config, error) {
	cfg, err := loadRaw(path)
	if err != nil {
		return nil, err
	}
	if cfg.Auth.Secret == "" {
		return nil, fmt.Errorf("auth.secret must be set (SHINYHUB_AUTH_SECRET)")
	}
	if cfg.Auth.Secret == "change-me-to-a-random-string" {
		return nil, fmt.Errorf("auth.secret is the placeholder value from shinyhub.yaml.example; generate a strong value with: openssl rand -hex 32")
	}
	if len(cfg.Auth.Secret) < 32 {
		return nil, fmt.Errorf("auth.secret must be at least 32 characters (got %d); generate one with: openssl rand -hex 32", len(cfg.Auth.Secret))
	}
	// Lockout guard: SSO-only (auth.local_login: false) with no SSO login path
	// configured would leave no way for anyone to sign in. Fail fast rather than
	// boot an unreachable server. (LoadForMaintenance skips this - backup/restore
	// performs no login.)
	if !cfg.Auth.LocalLoginEnabled() && !cfg.HasSSOLoginPath() {
		return nil, fmt.Errorf("auth.local_login is false but no SSO login path is configured (GitHub, Google, OIDC, or forward-auth); this would lock out all users. Configure an SSO provider or set auth.local_login: true")
	}
	return cfg, nil
}

// LoadForMaintenance loads the config the same way Load does but skips the
// auth.secret validation. Backup and restore operate only on files and the
// SQLite database; they perform no cryptography and therefore do not need a
// valid secret. Callers must not use cfg.Auth.Secret for any purpose.
func LoadForMaintenance(path string) (*Config, error) {
	cfg, err := loadRaw(path)
	if err != nil {
		return nil, err
	}
	// auth.secret is intentionally not validated here. The field may be empty,
	// the placeholder, or too short; none of those conditions affect backup or
	// restore correctness.
	return cfg, nil
}

// loadRaw is the shared implementation of Load and LoadForMaintenance. It
// parses, normalizes, and validates all config fields except auth.secret, which
// the two public entry points handle differently.
func loadRaw(path string) (*Config, error) {
	raw := &rawConfig{
		Database: DatabaseConfig{Driver: "sqlite", DSN: "./data/shinyhub.db"},
		Server:   ServerConfig{Host: "0.0.0.0", Port: 8080},
		Storage:  StorageConfig{AppsDir: "./data/apps", AppDataDir: "./data/app-data", MaxBundleMB: 128},
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("open config: %w", err)
		}
		if err == nil {
			// Decode via a streaming Decoder (not yaml.Unmarshal) so an empty
			// or comment-only file yields io.EOF and fails loud instead of
			// silently loading defaults - a botched mount or truncated write
			// must not start the server on defaults. The same decoder is reused
			// below to detect a trailing document; the bytes feed the top-level
			// key check.
			dec := yaml.NewDecoder(bytes.NewReader(data))
			if derr := dec.Decode(raw); derr != nil {
				return nil, fmt.Errorf("parse config: %w", derr)
			}
			// Reject misplaced/misspelled top-level keys (e.g. a `snapshot:` block
			// that belongs under `runtime:`). YAML's recursive strict mode is too
			// broad, so this is scoped to the top level - the level operators most
			// often get wrong, where a silent drop is invisible.
			if kerr := checkTopLevelKeys(data); kerr != nil {
				return nil, kerr
			}
			// Reject a trailing YAML document: the decode above read only the
			// first, so a "---"-separated document would be silently ignored -
			// another silent drop. A single leading "---" is one document and
			// this second Decode returns io.EOF.
			if derr := dec.Decode(new(yaml.Node)); derr == nil {
				return nil, fmt.Errorf("config: multiple YAML documents found; the config must be a single document (remove the '---' separator)")
			} else if !errors.Is(derr, io.EOF) {
				return nil, fmt.Errorf("parse config: %w", derr)
			}
		}
	}

	lc, err := parseLifecycle(raw.Lifecycle)
	if err != nil {
		return nil, err
	}

	rc, err := parseRuntime(raw.Runtime)
	if err != nil {
		return nil, err
	}

	histWindow, histInterval, err := parseMetricsHistory(raw.Metrics)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Database:  raw.Database,
		Server:    raw.Server,
		Auth:      raw.Auth,
		Storage:   raw.Storage,
		Lifecycle: lc,
		Runtime:   rc,
		Scheduler: SchedulerConfig{DefaultTimezone: raw.Scheduler.Timezone},
		Defaults:  DefaultsConfig{AppVisibility: raw.Defaults.AppVisibility},
		Tracing: TracingConfig{
			Enabled:      raw.Tracing.Enabled,
			OTLPEndpoint: raw.Tracing.OTLPEndpoint,
			OTLPProtocol: raw.Tracing.OTLPProtocol,
			OTLPHeaders:  raw.Tracing.OTLPHeaders,
			// Defaults applied here (not in normalizeTracing) so an explicit 0
			// from YAML survives; an env override is layered on top afterwards.
			SampleRatio:        derefOr(raw.Tracing.SampleRatio, 0.1),
			SlowRequestMS:      derefOr(raw.Tracing.SlowRequestMS, 1000),
			RingBufferSize:     derefOr(raw.Tracing.RingBufferSize, 200),
			TraceLinkTemplate:  raw.Tracing.TraceLinkTemplate,
			AutoInstrumentApps: raw.Tracing.AutoInstrumentApps,
		},
		Metrics: MetricsConfig{
			Enabled:         raw.Metrics.Enabled,
			Addr:            raw.Metrics.Addr,
			HistoryWindow:   histWindow,
			HistoryInterval: histInterval,
		},
		Maintenance: raw.Maintenance,
		Branding:    raw.Branding,
		Worker:      raw.Worker,
		OAuth: OAuthConfig{
			GitHub: GitHubOAuthConfig{
				ClientID:     raw.OAuth.GitHub.ClientID,
				ClientSecret: raw.OAuth.GitHub.ClientSecret,
				CallbackURL:  raw.OAuth.GitHub.CallbackURL,
			},
			Google: GoogleOAuthConfig{
				ClientID:     raw.OAuth.Google.ClientID,
				ClientSecret: raw.OAuth.Google.ClientSecret,
				CallbackURL:  raw.OAuth.Google.CallbackURL,
			},
			OIDC: OIDCConfig{
				IssuerURL:          raw.OAuth.OIDC.IssuerURL,
				ClientID:           raw.OAuth.OIDC.ClientID,
				ClientSecret:       raw.OAuth.OIDC.ClientSecret,
				CallbackURL:        raw.OAuth.OIDC.CallbackURL,
				DisplayName:        raw.OAuth.OIDC.DisplayName,
				GroupsClaim:        raw.OAuth.OIDC.GroupsClaim,
				GroupsScope:        raw.OAuth.OIDC.GroupsScope,
				RequireValidGroups: raw.OAuth.OIDC.RequireValidGroups,
			},
		},
	}
	if err := applyEnv(cfg); err != nil {
		return nil, err
	}

	// Validate the listen port range. Port 0 is allowed here (OS-assigned), but
	// the serve command further rejects it because zero-downtime upgrades need a
	// fixed port; a negative or out-of-range value is always a misconfiguration.
	if cfg.Server.Port < 0 || cfg.Server.Port > 65535 {
		return nil, fmt.Errorf("server.port must be between 0 and 65535, got %d", cfg.Server.Port)
	}

	// Parse trusted proxy CIDRs. Default to loopback-only when none are configured,
	// so XFF is trusted only from local reverse proxies by default.
	if len(cfg.Server.TrustedProxies) == 0 {
		cfg.Server.TrustedProxies = []string{"127.0.0.0/8", "::1/128"}
	}
	for _, cidr := range cfg.Server.TrustedProxies {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("trusted_proxies: invalid CIDR %q: %w", cidr, err)
		}
		cfg.TrustedProxyNets = append(cfg.TrustedProxyNets, ipNet)
	}

	if cfg.Server.ShutdownApps == "" {
		cfg.Server.ShutdownApps = "adopt"
	}
	switch cfg.Server.ShutdownApps {
	case "adopt", "stop":
		// allowed
	default:
		return nil, fmt.Errorf("server.shutdown_apps: %q is not allowed; must be \"adopt\" or \"stop\"",
			cfg.Server.ShutdownApps)
	}

	if cfg.Server.InstanceID == "" {
		host, hostErr := os.Hostname()
		if hostErr != nil || host == "" {
			host = "shinyhub"
		}
		cfg.Server.InstanceID = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	if cfg.Server.LeaseRenewEvery <= 0 {
		cfg.Server.LeaseRenewEvery = 10 * time.Second
	}
	if cfg.Server.LeaseTTL <= 0 {
		cfg.Server.LeaseTTL = 30 * time.Second
	}
	if cfg.Server.DrainTimeout <= 0 {
		cfg.Server.DrainTimeout = 60 * time.Second
	}
	if cfg.Server.StopGrace <= 0 {
		cfg.Server.StopGrace = 10 * time.Second
	}
	if cfg.Server.UpgradeTimeout <= 0 {
		cfg.Server.UpgradeTimeout = 60 * time.Second
	}

	if cfg.OAuth.OIDC.DisplayName == "" && cfg.OAuth.OIDC.IssuerURL != "" {
		cfg.OAuth.OIDC.DisplayName = "Sign in with SSO"
	}
	if cfg.Storage.VersionRetention <= 0 {
		cfg.Storage.VersionRetention = 5
	}
	if cfg.Storage.AppQuotaMB < 0 {
		cfg.Storage.AppQuotaMB = 0
	}
	if cfg.Maintenance.Interval <= 0 {
		cfg.Maintenance.Interval = time.Hour
	}
	if cfg.Maintenance.AuditRetentionDays < 0 {
		cfg.Maintenance.AuditRetentionDays = 0
	}
	if cfg.Maintenance.ScheduleRunRetentionCount < 0 {
		cfg.Maintenance.ScheduleRunRetentionCount = 0
	}
	if cfg.Storage.AppDataDir == "" {
		cfg.Storage.AppDataDir = "./data/app-data"
	}
	if cfg.Storage.MaxBundleMB < 0 {
		cfg.Storage.MaxBundleMB = 128
	}
	if cfg.Auth.DeployToken != "" {
		if cfg.Auth.DeployTokenRole == "" {
			cfg.Auth.DeployTokenRole = "developer"
		}
		switch cfg.Auth.DeployTokenRole {
		case "viewer", "developer", "operator", "admin":
		default:
			return nil, fmt.Errorf("auth.deploy_token_role: %q is not allowed; must be one of viewer, developer, operator, admin",
				cfg.Auth.DeployTokenRole)
		}
	}
	if cfg.Auth.OAuthDefaultRole == "" {
		cfg.Auth.OAuthDefaultRole = "viewer"
	}
	switch cfg.Auth.OAuthDefaultRole {
	case "viewer", "developer", "operator":
		// allowed
	default:
		return nil, fmt.Errorf("auth.oauth_default_role: %q is not allowed; must be one of viewer, developer, operator", cfg.Auth.OAuthDefaultRole)
	}
	if cfg.Auth.ForwardAuth.Enabled {
		if cfg.Auth.ForwardAuth.UserHeader == "" {
			cfg.Auth.ForwardAuth.UserHeader = "X-Forwarded-User"
		}
		if cfg.Auth.ForwardAuth.DefaultRole == "" {
			cfg.Auth.ForwardAuth.DefaultRole = "developer"
		}
		switch cfg.Auth.ForwardAuth.DefaultRole {
		case "viewer", "developer", "operator", "admin":
		default:
			return nil, fmt.Errorf("auth.forward_auth.default_role: invalid role %q", cfg.Auth.ForwardAuth.DefaultRole)
		}
	}
	// Default the OIDC groups claim when OIDC is configured.
	if cfg.OAuth.OIDC.IssuerURL != "" && cfg.OAuth.OIDC.GroupsClaim == "" {
		cfg.OAuth.OIDC.GroupsClaim = "groups"
	}
	// Merge the deprecated forward_auth.admin_groups into group_role_mappings as
	// role=admin (an explicit group_role_mappings entry for the same group wins).
	for _, g := range cfg.Auth.ForwardAuth.AdminGroups {
		exists := false
		for _, m := range cfg.Auth.GroupRoleMappings {
			if m.Group == g {
				exists = true
				break
			}
		}
		if !exists {
			cfg.Auth.GroupRoleMappings = append(cfg.Auth.GroupRoleMappings, GroupRoleMapping{Group: g, Role: "admin"})
		}
	}
	// Validate every mapped role (all four global roles are allowed; groups may
	// legitimately confer admin, unlike the JIT default).
	for _, m := range cfg.Auth.GroupRoleMappings {
		switch m.Role {
		case "viewer", "developer", "operator", "admin":
		default:
			return nil, fmt.Errorf("auth.group_role_mappings: %q is not a valid role for group %q", m.Role, m.Group)
		}
	}
	if cfg.Defaults.AppVisibility == "" {
		cfg.Defaults.AppVisibility = "private"
	}
	if !db.IsValidAppVisibility(cfg.Defaults.AppVisibility) {
		return nil, fmt.Errorf("defaults.app_visibility: %q is not allowed; must be one of %s",
			cfg.Defaults.AppVisibility, strings.Join(db.ValidAppVisibilities, ", "))
	}
	switch cfg.Runtime.Mode {
	case "native", "docker":
		// allowed
	default:
		return nil, fmt.Errorf("runtime.mode: %q is not supported; must be one of native, docker", cfg.Runtime.Mode)
	}
	switch cfg.Runtime.Docker.NetworkMode {
	case "bridge", "host":
		// allowed
	default:
		return nil, fmt.Errorf("runtime.docker.network_mode: %q is not supported; must be one of bridge, host", cfg.Runtime.Docker.NetworkMode)
	}
	if f := cfg.Runtime.Snapshot.ReclaimMinFraction; f <= 0 || f > 1 {
		return nil, fmt.Errorf("runtime.snapshot.reclaim_min_fraction: %v must be in (0, 1]", f)
	}
	if cfg.Runtime.Snapshot.MaxSuspended <= 0 {
		cfg.Runtime.Snapshot.MaxSuspended = 16
	}
	// Tiers: copy from raw, or synthesize a single default tier from Mode.
	if len(raw.Runtime.Tiers) == 0 {
		cfg.Runtime.Tiers = []TierConfig{{Name: "local", Runtime: cfg.Runtime.Mode}}
	} else {
		seen := map[string]bool{}
		hasFargate := false
		for _, t := range raw.Runtime.Tiers {
			if t.Name == "" {
				return nil, fmt.Errorf("runtime.tiers: tier name must not be empty")
			}
			if seen[t.Name] {
				return nil, fmt.Errorf("runtime.tiers: duplicate tier name %q", t.Name)
			}
			seen[t.Name] = true
			switch t.Runtime {
			case "native", "docker":
				// supported this phase
			case "remote_docker":
				// accepted: a remoteRuntime is registered for this tier at startup
			case "fargate":
				// accepted: a fargate runtime is registered for this tier at startup
				hasFargate = true
			default:
				return nil, fmt.Errorf("runtime.tiers: tier %q has unknown runtime %q (want native, docker, remote_docker, or fargate)", t.Name, t.Runtime)
			}
			lt := strings.ToUpper(strings.TrimSpace(t.LaunchType))
			switch lt {
			case "", "FARGATE", "EC2":
				// valid; empty is resolved after applyEnv below
			default:
				return nil, fmt.Errorf("runtime.tiers: tier %q has unknown launch_type %q (want FARGATE or EC2)", t.Name, lt)
			}
			cfg.Runtime.Tiers = append(cfg.Runtime.Tiers, TierConfig{Name: t.Name, Runtime: t.Runtime, LaunchType: lt})
		}
		if hasFargate {
			// Apply the default launch type from env or "FARGATE" to fargate tiers
			// that did not specify one. This must happen before validateFargate so
			// the matrix and platform_version checks see the resolved launch types.
			defaultLaunchType := "FARGATE"
			if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_LAUNCH_TYPE"); v != "" {
				u := strings.ToUpper(strings.TrimSpace(v))
				switch u {
				case "FARGATE", "EC2":
					defaultLaunchType = u
				default:
					return nil, fmt.Errorf("SHINYHUB_RUNTIME_FARGATE_LAUNCH_TYPE: %q is not valid (want FARGATE or EC2)", v)
				}
			}
			for i := range cfg.Runtime.Tiers {
				if cfg.Runtime.Tiers[i].Runtime == "fargate" && cfg.Runtime.Tiers[i].LaunchType == "" {
					cfg.Runtime.Tiers[i].LaunchType = defaultLaunchType
				}
			}
			if err := validateFargate(cfg.Runtime.Fargate, cfg.Runtime.Tiers); err != nil {
				return nil, err
			}
		}
	}
	if err := normalizeTracing(&cfg.Tracing); err != nil {
		return nil, err
	}
	if err := normalizeMetrics(&cfg.Metrics); err != nil {
		return nil, err
	}
	if err := validateMetricsHistory(cfg.Metrics.HistoryWindow, cfg.Metrics.HistoryInterval); err != nil {
		return nil, err
	}
	// Resolve scheduler timezone. Default to UTC when unset; validate when set.
	if cfg.Scheduler.DefaultTimezone == "" {
		cfg.Scheduler.DefaultTimezone = "UTC"
	}
	{
		loc, err := time.LoadLocation(cfg.Scheduler.DefaultTimezone)
		if err != nil {
			return nil, fmt.Errorf("scheduler.timezone: %q is not a valid IANA timezone: %w",
				cfg.Scheduler.DefaultTimezone, err)
		}
		cfg.Scheduler.Location = loc
	}
	if err := validateBranding(&cfg.Branding); err != nil {
		return nil, err
	}
	// Normalize storage roots to absolute so a relative/cwd-dependent root cannot
	// resolve differently across HA instances sharing one filesystem.
	if abs, err := filepath.Abs(cfg.Storage.AppsDir); err == nil {
		cfg.Storage.AppsDir = abs
	}
	if abs, err := filepath.Abs(cfg.Storage.AppDataDir); err == nil {
		cfg.Storage.AppDataDir = abs
	}
	return cfg, nil
}

// normalizeTracing applies defaults and validates the tracing block. When
// Enabled is false the block is left mostly untouched so disabled config still
// round-trips through Load (callers should not read fields without checking
// Enabled).
func normalizeTracing(t *TracingConfig) error {
	// Auto-instrumentation with tracing disabled is a broken half-mode: apps
	// would be wrapped in opentelemetry-instrument but receive no OTEL_*
	// env and export nowhere. Reject it loudly (checked before the Enabled
	// early-return below, which skips the rest of this validation).
	if t.AutoInstrumentApps && !t.Enabled {
		return fmt.Errorf("tracing.auto_instrument_apps requires tracing.enabled (SHINYHUB_TRACING_ENABLED)")
	}
	if !t.Enabled {
		return nil
	}
	// Enabling tracing without a collector endpoint is a broken half-mode: the
	// proxy would propagate traceparent but apps would receive no
	// OTEL_EXPORTER_OTLP_ENDPOINT and export nothing. Reject it loudly.
	if t.OTLPEndpoint == "" {
		return fmt.Errorf("tracing.otlp_endpoint must be set when tracing is enabled (SHINYHUB_TRACING_OTLP_ENDPOINT)")
	}
	if t.OTLPProtocol == "" {
		t.OTLPProtocol = "http/protobuf"
	}
	switch t.OTLPProtocol {
	case "http/protobuf", "grpc":
	default:
		return fmt.Errorf("tracing.otlp_protocol: %q is not supported; must be one of http/protobuf, grpc", t.OTLPProtocol)
	}
	// Defaults for SampleRatio/SlowRequestMS/RingBufferSize are resolved at decode
	// time (see Load), so an explicit 0 reaches here intact and carries its
	// documented meaning (0 disables sampling / retains only error spans /
	// disables the ring buffer). Here we only validate ranges.
	if t.SampleRatio < 0 || t.SampleRatio > 1 {
		return fmt.Errorf("tracing.sample_ratio: %g is out of range; must be between 0 and 1", t.SampleRatio)
	}
	if t.SlowRequestMS < 0 {
		return fmt.Errorf("tracing.slow_request_ms: %d is negative", t.SlowRequestMS)
	}
	if t.RingBufferSize < 0 {
		return fmt.Errorf("tracing.ring_buffer_size: %d is negative", t.RingBufferSize)
	}
	if t.TraceLinkTemplate != "" && !strings.Contains(t.TraceLinkTemplate, "{trace_id}") {
		return fmt.Errorf("tracing.trace_link_template: %q is missing the {trace_id} placeholder; links would be broken", t.TraceLinkTemplate)
	}
	return nil
}

// normalizeMetrics applies the loopback default and validates the listen
// address. When Enabled is false the block is left untouched so a malformed
// addr in a disabled block never blocks startup.
func normalizeMetrics(m *MetricsConfig) error {
	if !m.Enabled {
		return nil
	}
	if m.Addr == "" {
		m.Addr = "127.0.0.1:9090"
	}
	if _, _, err := net.SplitHostPort(m.Addr); err != nil {
		return fmt.Errorf("metrics.addr: %q is not a valid host:port address: %w", m.Addr, err)
	}
	return nil
}

// Bounds for the in-memory app-metrics history. The floors keep a misconfigured
// tiny interval from driving runaway sampling; the ceilings (plus the
// history.Store's hard per-app point cap) keep memory bounded.
const (
	defaultHistoryWindow   = 12 * time.Hour
	defaultHistoryInterval = 15 * time.Second
	minHistoryWindow       = 1 * time.Minute
	maxHistoryWindow       = 48 * time.Hour
	minHistoryInterval     = 1 * time.Second
	maxHistoryInterval     = 10 * time.Minute
)

// parseMetricsHistory parses the raw history duration strings, applying defaults
// for absent keys (an empty string means the key was absent; "0s" is the
// explicit disable for the window). Validation is deferred until after env
// overrides are layered on, so an env-supplied value is the one validated, not
// the YAML value it replaces.
func parseMetricsHistory(raw rawMetricsConfig) (window, interval time.Duration, err error) {
	window = defaultHistoryWindow
	if raw.HistoryWindow != "" {
		window, err = time.ParseDuration(raw.HistoryWindow)
		if err != nil {
			return 0, 0, fmt.Errorf("metrics.history_window: %q is not a duration: %w", raw.HistoryWindow, err)
		}
	}
	interval = defaultHistoryInterval
	if raw.HistoryInterval != "" {
		interval, err = time.ParseDuration(raw.HistoryInterval)
		if err != nil {
			return 0, 0, fmt.Errorf("metrics.history_interval: %q is not a duration: %w", raw.HistoryInterval, err)
		}
	}
	return window, interval, nil
}

// validateMetricsHistory enforces the history window/interval bounds. A window of
// 0 (collection disabled) is always accepted.
func validateMetricsHistory(window, interval time.Duration) error {
	if window != 0 && (window < minHistoryWindow || window > maxHistoryWindow) {
		return fmt.Errorf("metrics.history_window: %v is out of range [%v, %v] (use 0 to disable)",
			window, minHistoryWindow, maxHistoryWindow)
	}
	if interval < minHistoryInterval || interval > maxHistoryInterval {
		return fmt.Errorf("metrics.history_interval: %v is out of range [%v, %v]",
			interval, minHistoryInterval, maxHistoryInterval)
	}
	return nil
}

// derefOr returns *p when p is non-nil, otherwise def. It lets an explicitly
// configured zero survive while an absent key falls back to a default.
func derefOr[T any](p *T, def T) T {
	if p != nil {
		return *p
	}
	return def
}

// parseBoolEnv parses a boolean-ish environment variable, accepting the common
// truthy/falsy spellings operators reach for. An unrecognized value is an error
// rather than a silent false, so a typo surfaces at startup.
func parseBoolEnv(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "y":
		return true, nil
	case "0", "false", "no", "off", "n":
		return false, nil
	default:
		return false, fmt.Errorf("%q is not a boolean (use true/false, yes/no, on/off, 1/0)", v)
	}
}

// parseGroupRoleMappings parses "group:role,group2:role2" into mappings.
func parseGroupRoleMappings(v string) ([]GroupRoleMapping, error) {
	out := []GroupRoleMapping{}
	for _, pair := range splitCSV(v) {
		g, r, ok := strings.Cut(pair, ":")
		g, r = strings.TrimSpace(g), strings.TrimSpace(r)
		if !ok || g == "" || r == "" {
			return nil, fmt.Errorf("invalid mapping %q (want group:role)", pair)
		}
		out = append(out, GroupRoleMapping{Group: g, Role: r})
	}
	return out, nil
}

// splitCSV splits a comma-separated env value into trimmed, non-empty entries.
func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func parseLifecycle(r rawLifecycleConfig) (LifecycleConfig, error) {
	lc := LifecycleConfig{
		WatchInterval:      15 * time.Second,
		RestartMaxAttempts: 5,
		HibernateTimeout:   30 * time.Minute,
		WakeHold:           5 * time.Second,
	}
	if r.WatchInterval != "" {
		d, err := time.ParseDuration(r.WatchInterval)
		if err != nil {
			return lc, fmt.Errorf("lifecycle.watch_interval: %w", err)
		}
		lc.WatchInterval = d
	}
	if r.RestartMaxAttempts != 0 {
		lc.RestartMaxAttempts = r.RestartMaxAttempts
	}
	if r.HibernateTimeout != "" {
		d, err := time.ParseDuration(r.HibernateTimeout)
		if err != nil {
			return lc, fmt.Errorf("lifecycle.hibernate_timeout: %w", err)
		}
		lc.HibernateTimeout = d
	}
	if r.WakeHold != "" {
		d, err := time.ParseDuration(r.WakeHold)
		if err != nil {
			return lc, fmt.Errorf("lifecycle.wake_hold: %w", err)
		}
		lc.WakeHold = d // may be 0 to disable the hold
	}
	return lc, nil
}

// Docker runtime defaults, shared between the control-plane config loader and
// the standalone worker command so both agree on the same baseline images,
// socket, and network mode without duplicating the literals.
const (
	DefaultDockerSocket = "/var/run/docker.sock"
	DefaultPythonImage  = "ghcr.io/astral-sh/uv:python3.12-bookworm-slim"
	DefaultRImage       = "rocker/r-base"
	DefaultNetworkMode  = "bridge"
)

// fargateMatrixEntry defines the valid memory range for a given Fargate CPU
// tier. For the 256-unit tier, allowedMem is the exhaustive discrete set
// {512, 1024, 2048}. For all other tiers, memMin/memMax/memInc define the valid
// range and increment: valid when memMin <= mem <= memMax AND
// (mem - memMin) % memInc == 0.
type fargateMatrixEntry struct {
	allowedMem []int // non-nil: discrete set (256 CPU only)
	memMin     int
	memMax     int
	memInc     int
}

var fargateMatrix = map[int]fargateMatrixEntry{
	256:   {allowedMem: []int{512, 1024, 2048}},
	512:   {memMin: 1024, memMax: 4096, memInc: 1024},
	1024:  {memMin: 2048, memMax: 8192, memInc: 1024},
	2048:  {memMin: 4096, memMax: 16384, memInc: 1024},
	4096:  {memMin: 8192, memMax: 30720, memInc: 1024},
	8192:  {memMin: 16384, memMax: 61440, memInc: 4096},
	16384: {memMin: 32768, memMax: 122880, memInc: 8192},
}

// validateFargate enforces the settings a fargate tier cannot run without. It is
// only called when at least one tier declares runtime "fargate", so a config
// with no fargate tier never needs the block populated. The tiers slice is used
// to determine which checks apply: the Fargate CPU/memory matrix check only
// applies to FARGATE launch-type tiers; EC2 tiers are not matrix-constrained.
func validateFargate(f FargateRuntimeConfig, tiers []TierConfig) error {
	if f.Cluster == "" {
		return fmt.Errorf("runtime.fargate.cluster is required when a tier uses runtime \"fargate\"")
	}
	if f.TaskDefinition == "" {
		return fmt.Errorf("runtime.fargate.task_definition is required when a tier uses runtime \"fargate\"")
	}
	if f.ContainerName == "" {
		return fmt.Errorf("runtime.fargate.container_name is required when a tier uses runtime \"fargate\"")
	}
	if len(f.Subnets) == 0 {
		return fmt.Errorf("runtime.fargate.subnets must list at least one subnet when a tier uses runtime \"fargate\"")
	}
	if f.RouteViaPublicIP && !f.AssignPublicIP {
		return fmt.Errorf("runtime.fargate.route_via_public_ip requires assign_public_ip: true")
	}
	if f.ControlPlaneURL == "" {
		return fmt.Errorf("runtime.fargate.control_plane_url is required when a tier uses runtime \"fargate\"")
	}
	if f.RouteViaPublicIP && !strings.HasPrefix(f.ControlPlaneURL, "https://") {
		return fmt.Errorf("runtime.fargate.control_plane_url must use https:// when route_via_public_ip is true (a plaintext bundle token over a public channel is replayable within the TTL)")
	}

	// Determine which fargate tiers use FARGATE vs EC2 launch type.
	// allEC2 requires at least one fargate tier to be present so an empty
	// tiers slice (or a slice with no fargate tiers) does not spuriously
	// reject platform_version.
	hasFargateLT := false
	fargateTierCount := 0
	for _, t := range tiers {
		if t.Runtime != "fargate" {
			continue
		}
		fargateTierCount++
		if t.LaunchType != "EC2" {
			hasFargateLT = true
		}
	}
	allEC2 := fargateTierCount > 0 && !hasFargateLT

	// platform_version is Fargate-only; ECS rejects it for EC2 tasks.
	if allEC2 && f.PlatformVersion != "" {
		return fmt.Errorf("runtime.fargate.platform_version must not be set when all fargate tiers use launch_type EC2 (ECS rejects PlatformVersion for EC2 tasks)")
	}

	// The Fargate CPU/memory matrix only applies when at least one FARGATE-
	// launch-type tier exists. EC2 tiers are not matrix-constrained.
	if hasFargateLT {
		if f.TaskCPUUnits == 0 {
			return fmt.Errorf("runtime.fargate.task_cpu_units is required when a tier uses runtime \"fargate\" with FARGATE launch type")
		}
		if f.TaskMemoryMB == 0 {
			return fmt.Errorf("runtime.fargate.task_memory_mb is required when a tier uses runtime \"fargate\" with FARGATE launch type")
		}
		entry, ok := fargateMatrix[f.TaskCPUUnits]
		if !ok {
			allowed := slices.Sorted(maps.Keys(fargateMatrix))
			parts := make([]string, len(allowed))
			for i, v := range allowed {
				parts[i] = strconv.Itoa(v)
			}
			return fmt.Errorf("runtime.fargate.task_cpu_units: %d is not a valid Fargate CPU value; must be one of %s",
				f.TaskCPUUnits, strings.Join(parts, ", "))
		}
		if entry.allowedMem != nil {
			// Discrete set (256-unit tier).
			valid := false
			for _, m := range entry.allowedMem {
				if f.TaskMemoryMB == m {
					valid = true
					break
				}
			}
			if !valid {
				return fmt.Errorf("runtime.fargate.task_memory_mb: %d is not valid for cpu_units=256; must be one of 512, 1024, 2048", f.TaskMemoryMB)
			}
		} else {
			if f.TaskMemoryMB < entry.memMin || f.TaskMemoryMB > entry.memMax {
				return fmt.Errorf("runtime.fargate.task_memory_mb: %d is out of range for cpu_units=%d; must be between %d and %d",
					f.TaskMemoryMB, f.TaskCPUUnits, entry.memMin, entry.memMax)
			}
			if (f.TaskMemoryMB-entry.memMin)%entry.memInc != 0 {
				return fmt.Errorf("runtime.fargate.task_memory_mb: %d is not a valid increment for cpu_units=%d; must be %d + n*%d (n>=0)",
					f.TaskMemoryMB, f.TaskCPUUnits, entry.memMin, entry.memInc)
			}
		}
	}

	if f.S3Files.Configured() {
		if !strings.Contains(f.S3Files.FileSystemArn, ":s3files:") ||
			!strings.Contains(f.S3Files.FileSystemArn, ":file-system/") {
			return fmt.Errorf("runtime.fargate.s3files.file_system_arn %q is not an S3 Files file-system ARN (arn:aws:s3files:<region>:<account>:file-system/fs-...)", f.S3Files.FileSystemArn)
		}
		if f.S3Files.AccessPointArn != "" && !strings.Contains(f.S3Files.AccessPointArn, ":access-point/") {
			return fmt.Errorf("runtime.fargate.s3files.access_point_arn %q is not an S3 Files access-point ARN", f.S3Files.AccessPointArn)
		}
		if !strings.HasPrefix(f.S3Files.MountPath, "/") {
			return fmt.Errorf("runtime.fargate.s3files.mount_path %q must be an absolute path (the app's working directory + \"/data\", e.g. /app/bundle/data)", f.S3Files.MountPath)
		}
		if f.S3Files.TransitEncryptionPort < 0 || f.S3Files.TransitEncryptionPort > 65535 {
			return fmt.Errorf("runtime.fargate.s3files.transit_encryption_port %d is out of range (0 lets ECS choose; 1-65535 otherwise)", f.S3Files.TransitEncryptionPort)
		}
	}
	return nil
}

func parseRuntime(r rawRuntimeConfig) (RuntimeConfig, error) {
	rc := RuntimeConfig{
		Mode: "native",
		Autoscale: AutoscaleConfig{
			ScanInterval:  30 * time.Second,
			Cooldown:      3 * time.Minute,
			DefaultTarget: 0.8,
		},
		Docker: DockerRuntimeConfig{
			Socket: DefaultDockerSocket,
			Images: DockerImages{
				Python: DefaultPythonImage,
				R:      DefaultRImage,
			},
			NetworkMode: DefaultNetworkMode,
		},
		Snapshot: SnapshotConfig{MaxSuspended: 16, ReclaimMinFraction: 0.8, RestoreOnStartup: true},
	}
	if r.Mode != "" {
		rc.Mode = r.Mode
	}
	isolation, err := sandbox.ParseLevel(r.Native.Isolation)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("runtime.native.isolation: %w", err)
	}
	rc.Native.Isolation = string(isolation)
	rc.Snapshot.Enabled = r.Snapshot.Enabled
	if r.Snapshot.MaxSuspended > 0 {
		rc.Snapshot.MaxSuspended = r.Snapshot.MaxSuspended
	}
	if r.Snapshot.ReclaimMinFraction != 0 {
		rc.Snapshot.ReclaimMinFraction = r.Snapshot.ReclaimMinFraction
	}
	if r.Snapshot.RestoreOnStartup != nil {
		rc.Snapshot.RestoreOnStartup = *r.Snapshot.RestoreOnStartup
	}
	if r.Docker.Socket != "" {
		rc.Docker.Socket = r.Docker.Socket
	}
	if r.Docker.NetworkMode != "" {
		rc.Docker.NetworkMode = r.Docker.NetworkMode
	}
	if r.Docker.Images.Python != "" {
		rc.Docker.Images.Python = r.Docker.Images.Python
	}
	if r.Docker.Images.R != "" {
		rc.Docker.Images.R = r.Docker.Images.R
	}
	if r.Docker.DefaultMemoryMB != 0 {
		rc.Docker.DefaultMemoryMB = r.Docker.DefaultMemoryMB
	}
	if r.Docker.DefaultCPUPercent != 0 {
		rc.Docker.DefaultCPUPercent = r.Docker.DefaultCPUPercent
	}
	if r.DefaultReplicas > 0 {
		rc.DefaultReplicas = r.DefaultReplicas
	}
	if r.MaxReplicas > 0 {
		rc.MaxReplicas = r.MaxReplicas
	}
	if rc.DefaultReplicas <= 0 {
		rc.DefaultReplicas = 1
	}
	if rc.MaxReplicas <= 0 {
		rc.MaxReplicas = 32
	}
	// Profiling spike showed single-event-loop p99 stays healthy to c=10 and
	// degrades sharply beyond that, with some apps erroring at c=30. 10 is
	// conservative and safe as a platform-wide default applied only when the
	// operator does not set the key at all. An explicit 0 means "unlimited"
	// as documented; a negative value is treated the same as 0.
	switch {
	case r.DefaultMaxSessionsPerReplica == nil:
		rc.DefaultMaxSessionsPerReplica = 10
	case *r.DefaultMaxSessionsPerReplica < 0:
		rc.DefaultMaxSessionsPerReplica = 0
	default:
		rc.DefaultMaxSessionsPerReplica = *r.DefaultMaxSessionsPerReplica
	}

	switch r.DefaultWorkerIsolation {
	case string(IsolationGrouped), string(IsolationPerSession):
		rc.DefaultWorkerIsolation = r.DefaultWorkerIsolation
	default:
		rc.DefaultWorkerIsolation = string(IsolationMultiplex)
	}

	rc.Fargate = FargateRuntimeConfig{
		Cluster:           r.Fargate.Cluster,
		TaskDefinition:    r.Fargate.TaskDefinition,
		ContainerName:     r.Fargate.ContainerName,
		Subnets:           r.Fargate.Subnets,
		SecurityGroups:    r.Fargate.SecurityGroups,
		AssignPublicIP:    r.Fargate.AssignPublicIP,
		PlatformVersion:   r.Fargate.PlatformVersion,
		Region:            r.Fargate.Region,
		RouteViaPublicIP:  r.Fargate.RouteViaPublicIP,
		TaskCPUUnits:      r.Fargate.TaskCPUUnits,
		TaskMemoryMB:      r.Fargate.TaskMemoryMB,
		DefaultMemoryMB:   r.Fargate.DefaultMemoryMB,
		DefaultCPUPercent: r.Fargate.DefaultCPUPercent,
		ControlPlaneURL:   r.Fargate.ControlPlaneURL,
		DurableData:       r.Fargate.DurableData,
		S3Files: FargateS3FilesConfig{
			FileSystemArn:         r.Fargate.S3Files.FileSystemArn,
			RootDirectory:         r.Fargate.S3Files.RootDirectory,
			AccessPointArn:        r.Fargate.S3Files.AccessPointArn,
			TransitEncryptionPort: r.Fargate.S3Files.TransitEncryptionPort,
			MountPath:             r.Fargate.S3Files.MountPath,
		},
		SecretsNamePrefix: r.Fargate.Secrets.NamePrefix,
		SecretsKMSKeyID:   r.Fargate.Secrets.KMSKeyID,
	}
	// Apply S3 Files defaults only when the backend is configured.
	if rc.Fargate.S3Files.Configured() {
		if rc.Fargate.S3Files.RootDirectory == "" {
			rc.Fargate.S3Files.RootDirectory = "/"
		}
		if rc.Fargate.S3Files.MountPath == "" {
			rc.Fargate.S3Files.MountPath = "/app/bundle/data"
		}
	}
	if r.Fargate.BundleTokenTTL != "" {
		d, err := time.ParseDuration(r.Fargate.BundleTokenTTL)
		if err != nil {
			return rc, fmt.Errorf("runtime.fargate.bundle_token_ttl: %w", err)
		}
		rc.Fargate.BundleTokenTTL = d
	}
	if rc.Fargate.BundleTokenTTL <= 0 {
		rc.Fargate.BundleTokenTTL = 10 * time.Minute
	}

	rc.Autoscale.Enabled = r.Autoscale.Enabled
	if r.Autoscale.ScanInterval != "" {
		d, err := time.ParseDuration(r.Autoscale.ScanInterval)
		if err != nil {
			return rc, fmt.Errorf("runtime.autoscale.scan_interval: %w", err)
		}
		if d <= 0 {
			return rc, fmt.Errorf("runtime.autoscale.scan_interval: must be > 0, got %v", d)
		}
		rc.Autoscale.ScanInterval = d
	}
	if r.Autoscale.Cooldown != "" {
		d, err := time.ParseDuration(r.Autoscale.Cooldown)
		if err != nil {
			return rc, fmt.Errorf("runtime.autoscale.cooldown: %w", err)
		}
		if d <= 0 {
			return rc, fmt.Errorf("runtime.autoscale.cooldown: must be > 0, got %v", d)
		}
		rc.Autoscale.Cooldown = d
	}
	if r.Autoscale.DefaultTarget != nil {
		t := *r.Autoscale.DefaultTarget
		if t <= 0 || t > 1 {
			return rc, fmt.Errorf("runtime.autoscale.default_target: must be in (0,1], got %v", t)
		}
		rc.Autoscale.DefaultTarget = t
	}
	return rc, nil
}

func applyEnv(cfg *Config) error {
	if v := os.Getenv("SHINYHUB_AUTH_SECRET"); v != "" {
		cfg.Auth.Secret = v
	}
	if v := os.Getenv("SHINYHUB_AUTH_OAUTH_DEFAULT_ROLE"); v != "" {
		cfg.Auth.OAuthDefaultRole = v
	}
	if v := os.Getenv("SHINYHUB_DEPLOY_TOKEN"); v != "" {
		cfg.Auth.DeployToken = v
	}
	if v := os.Getenv("SHINYHUB_DEPLOY_TOKEN_ROLE"); v != "" {
		cfg.Auth.DeployTokenRole = v
	}
	if v := os.Getenv("SHINYHUB_FORWARD_AUTH_ENABLED"); v != "" {
		b, err := parseBoolEnv(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_FORWARD_AUTH_ENABLED: %w", err)
		}
		cfg.Auth.ForwardAuth.Enabled = b
	}
	if v := os.Getenv("SHINYHUB_FORWARD_AUTH_USER_HEADER"); v != "" {
		cfg.Auth.ForwardAuth.UserHeader = v
	}
	if v := os.Getenv("SHINYHUB_FORWARD_AUTH_EMAIL_HEADER"); v != "" {
		cfg.Auth.ForwardAuth.EmailHeader = v
	}
	if v := os.Getenv("SHINYHUB_FORWARD_AUTH_NAME_HEADER"); v != "" {
		cfg.Auth.ForwardAuth.NameHeader = v
	}
	if v := os.Getenv("SHINYHUB_FORWARD_AUTH_GROUPS_HEADER"); v != "" {
		cfg.Auth.ForwardAuth.GroupsHeader = v
	}
	if v := os.Getenv("SHINYHUB_FORWARD_AUTH_ADMIN_GROUPS"); v != "" {
		cfg.Auth.ForwardAuth.AdminGroups = splitCSV(v)
	}
	if v := os.Getenv("SHINYHUB_FORWARD_AUTH_DEFAULT_ROLE"); v != "" {
		cfg.Auth.ForwardAuth.DefaultRole = v
	}
	if v := os.Getenv("SHINYHUB_FORWARD_AUTH_REQUIRE_GROUPS_HEADER"); v != "" {
		b, err := parseBoolEnv(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_FORWARD_AUTH_REQUIRE_GROUPS_HEADER: %w", err)
		}
		cfg.Auth.ForwardAuth.RequireGroupsHeader = b
	}
	if v := os.Getenv("SHINYHUB_AUTH_GROUP_ROLE_MAPPINGS"); v != "" {
		ms, err := parseGroupRoleMappings(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_AUTH_GROUP_ROLE_MAPPINGS: %w", err)
		}
		cfg.Auth.GroupRoleMappings = ms
	}
	if v := os.Getenv("SHINYHUB_IDENTITY_HEADERS"); v != "" {
		b, err := parseBoolEnv(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_IDENTITY_HEADERS: %w", err)
		}
		cfg.Auth.IdentityHeaders = &b
	}
	if v := os.Getenv("SHINYHUB_AUTH_LOCAL_LOGIN"); v != "" {
		b, err := parseBoolEnv(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_AUTH_LOCAL_LOGIN: %w", err)
		}
		cfg.Auth.LocalLogin = &b
	}
	if v := os.Getenv("SHINYHUB_DB_DSN"); v != "" {
		cfg.Database.DSN = v
	}
	if v := os.Getenv("SHINYHUB_PID_FILE"); v != "" {
		cfg.Server.PIDFile = v
	}
	if v := os.Getenv("SHINYHUB_UPGRADE_TIMEOUT"); v != "" {
		d, perr := time.ParseDuration(v)
		if perr != nil {
			return fmt.Errorf("parse SHINYHUB_UPGRADE_TIMEOUT %q: %w", v, perr)
		}
		if d <= 0 {
			return fmt.Errorf("SHINYHUB_UPGRADE_TIMEOUT must be positive, got %q", v)
		}
		cfg.Server.UpgradeTimeout = d
	}
	if v := os.Getenv("SHINYHUB_STOP_GRACE"); v != "" {
		d, perr := time.ParseDuration(v)
		if perr != nil {
			return fmt.Errorf("parse SHINYHUB_STOP_GRACE %q: %w", v, perr)
		}
		if d <= 0 {
			return fmt.Errorf("SHINYHUB_STOP_GRACE must be positive, got %q", v)
		}
		cfg.Server.StopGrace = d
	}
	if v := os.Getenv("SHINYHUB_APPS_DIR"); v != "" {
		cfg.Storage.AppsDir = v
	}
	if v := os.Getenv("SHINYHUB_STORAGE_VERSION_RETENTION"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_STORAGE_VERSION_RETENTION: %q is not an integer: %w", v, err)
		}
		cfg.Storage.VersionRetention = n
	}
	if v := os.Getenv("SHINYHUB_AUDIT_RETENTION_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_AUDIT_RETENTION_DAYS: %q is not an integer: %w", v, err)
		}
		cfg.Maintenance.AuditRetentionDays = n
	}
	if v := os.Getenv("SHINYHUB_SCHEDULE_RUN_RETENTION_COUNT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_SCHEDULE_RUN_RETENTION_COUNT: %q is not an integer: %w", v, err)
		}
		cfg.Maintenance.ScheduleRunRetentionCount = n
	}
	if v := os.Getenv("SHINYHUB_APP_QUOTA_MB"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_APP_QUOTA_MB: %q is not an integer: %w", v, err)
		}
		cfg.Storage.AppQuotaMB = n
	}
	if v := os.Getenv("SHINYHUB_APP_DATA_DIR"); v != "" {
		cfg.Storage.AppDataDir = v
	}
	if v := os.Getenv("SHINYHUB_MAX_BUNDLE_MB"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_MAX_BUNDLE_MB: %q is not an integer: %w", v, err)
		}
		cfg.Storage.MaxBundleMB = n
	}
	if v := os.Getenv("SHINYHUB_BASE_URL"); v != "" {
		cfg.Server.BaseURL = v
	}
	if v := os.Getenv("SHINYHUB_SERVER_HOST"); v != "" {
		cfg.Server.Host = v
	}
	if v := os.Getenv("SHINYHUB_SERVER_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_SERVER_PORT: %q is not an integer: %w", v, err)
		}
		cfg.Server.Port = n
	}
	if v := os.Getenv("SHINYHUB_SERVER_HOST_BUDGET_MB"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_SERVER_HOST_BUDGET_MB: %q is not an integer: %w", v, err)
		}
		cfg.Server.HostBudgetMB = n
	}
	if v := os.Getenv("SHINYHUB_TRUSTED_PROXIES"); v != "" {
		cfg.Server.TrustedProxies = splitCSV(v)
	}
	if v := os.Getenv("SHINYHUB_SHUTDOWN_APPS"); v != "" {
		cfg.Server.ShutdownApps = v
	}
	if v := os.Getenv("SHINYHUB_GITHUB_CLIENT_ID"); v != "" {
		cfg.OAuth.GitHub.ClientID = v
	}
	if v := os.Getenv("SHINYHUB_GITHUB_CLIENT_SECRET"); v != "" {
		cfg.OAuth.GitHub.ClientSecret = v
	}
	if v := os.Getenv("SHINYHUB_GITHUB_CALLBACK_URL"); v != "" {
		cfg.OAuth.GitHub.CallbackURL = v
	}
	if v := os.Getenv("SHINYHUB_GOOGLE_CLIENT_ID"); v != "" {
		cfg.OAuth.Google.ClientID = v
	}
	if v := os.Getenv("SHINYHUB_GOOGLE_CLIENT_SECRET"); v != "" {
		cfg.OAuth.Google.ClientSecret = v
	}
	if v := os.Getenv("SHINYHUB_GOOGLE_CALLBACK_URL"); v != "" {
		cfg.OAuth.Google.CallbackURL = v
	}
	if v := os.Getenv("SHINYHUB_OIDC_ISSUER_URL"); v != "" {
		cfg.OAuth.OIDC.IssuerURL = v
	}
	if v := os.Getenv("SHINYHUB_OIDC_CLIENT_ID"); v != "" {
		cfg.OAuth.OIDC.ClientID = v
	}
	if v := os.Getenv("SHINYHUB_OIDC_CLIENT_SECRET"); v != "" {
		cfg.OAuth.OIDC.ClientSecret = v
	}
	if v := os.Getenv("SHINYHUB_OIDC_CALLBACK_URL"); v != "" {
		cfg.OAuth.OIDC.CallbackURL = v
	}
	if v := os.Getenv("SHINYHUB_OIDC_DISPLAY_NAME"); v != "" {
		cfg.OAuth.OIDC.DisplayName = v
	}
	if v := os.Getenv("SHINYHUB_OIDC_GROUPS_CLAIM"); v != "" {
		cfg.OAuth.OIDC.GroupsClaim = v
	}
	if v := os.Getenv("SHINYHUB_OIDC_GROUPS_SCOPE"); v != "" {
		cfg.OAuth.OIDC.GroupsScope = v
	}
	if v := os.Getenv("SHINYHUB_OIDC_REQUIRE_VALID_GROUPS"); v != "" {
		b, err := parseBoolEnv(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_OIDC_REQUIRE_VALID_GROUPS: %w", err)
		}
		cfg.OAuth.OIDC.RequireValidGroups = b
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_MODE"); v != "" {
		cfg.Runtime.Mode = v
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_NATIVE_ISOLATION"); v != "" {
		level, err := sandbox.ParseLevel(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_NATIVE_ISOLATION: %w", err)
		}
		cfg.Runtime.Native.Isolation = string(level)
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_DOCKER_SOCKET"); v != "" {
		cfg.Runtime.Docker.Socket = v
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_DOCKER_NETWORK_MODE"); v != "" {
		cfg.Runtime.Docker.NetworkMode = v
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_DOCKER_DEFAULT_MEMORY_MB"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_DOCKER_DEFAULT_MEMORY_MB: %q is not an integer: %w", v, err)
		}
		cfg.Runtime.Docker.DefaultMemoryMB = n
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_DOCKER_DEFAULT_CPU_PERCENT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_DOCKER_DEFAULT_CPU_PERCENT: %q is not an integer: %w", v, err)
		}
		cfg.Runtime.Docker.DefaultCPUPercent = n
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_SNAPSHOT_ENABLED"); v != "" {
		b, err := parseBoolEnv(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_SNAPSHOT_ENABLED: %w", err)
		}
		cfg.Runtime.Snapshot.Enabled = b
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_SNAPSHOT_MAX_SUSPENDED"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_SNAPSHOT_MAX_SUSPENDED: %q is not an integer: %w", v, err)
		}
		cfg.Runtime.Snapshot.MaxSuspended = n
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_SNAPSHOT_RECLAIM_MIN_FRACTION"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_SNAPSHOT_RECLAIM_MIN_FRACTION: %q is not a number: %w", v, err)
		}
		cfg.Runtime.Snapshot.ReclaimMinFraction = f
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_DOCKER_IMAGE_PYTHON"); v != "" {
		cfg.Runtime.Docker.Images.Python = v
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_DOCKER_IMAGE_R"); v != "" {
		cfg.Runtime.Docker.Images.R = v
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_CLUSTER"); v != "" {
		cfg.Runtime.Fargate.Cluster = v
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_TASK_DEFINITION"); v != "" {
		cfg.Runtime.Fargate.TaskDefinition = v
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_CONTAINER_NAME"); v != "" {
		cfg.Runtime.Fargate.ContainerName = v
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_SUBNETS"); v != "" {
		cfg.Runtime.Fargate.Subnets = splitCSV(v)
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_SECURITY_GROUPS"); v != "" {
		cfg.Runtime.Fargate.SecurityGroups = splitCSV(v)
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_ASSIGN_PUBLIC_IP"); v != "" {
		b, err := parseBoolEnv(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_FARGATE_ASSIGN_PUBLIC_IP: %w", err)
		}
		cfg.Runtime.Fargate.AssignPublicIP = b
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_PLATFORM_VERSION"); v != "" {
		cfg.Runtime.Fargate.PlatformVersion = v
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_REGION"); v != "" {
		cfg.Runtime.Fargate.Region = v
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_ROUTE_VIA_PUBLIC_IP"); v != "" {
		b, err := parseBoolEnv(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_FARGATE_ROUTE_VIA_PUBLIC_IP: %w", err)
		}
		cfg.Runtime.Fargate.RouteViaPublicIP = b
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_CONTROL_PLANE_URL"); v != "" {
		cfg.Runtime.Fargate.ControlPlaneURL = v
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_SECRETS_NAME_PREFIX"); v != "" {
		cfg.Runtime.Fargate.SecretsNamePrefix = v
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_SECRETS_KMS_KEY_ID"); v != "" {
		cfg.Runtime.Fargate.SecretsKMSKeyID = v
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_BUNDLE_TOKEN_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_FARGATE_BUNDLE_TOKEN_TTL: %q is not a duration: %w", v, err)
		}
		if d <= 0 {
			return fmt.Errorf("SHINYHUB_RUNTIME_FARGATE_BUNDLE_TOKEN_TTL: %q must be a positive duration", v)
		}
		cfg.Runtime.Fargate.BundleTokenTTL = d
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_TASK_CPU_UNITS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_FARGATE_TASK_CPU_UNITS: %q is not an integer: %w", v, err)
		}
		cfg.Runtime.Fargate.TaskCPUUnits = n
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_TASK_MEMORY_MB"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_FARGATE_TASK_MEMORY_MB: %q is not an integer: %w", v, err)
		}
		cfg.Runtime.Fargate.TaskMemoryMB = n
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_DEFAULT_MEMORY_MB"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_FARGATE_DEFAULT_MEMORY_MB: %q is not an integer: %w", v, err)
		}
		cfg.Runtime.Fargate.DefaultMemoryMB = n
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_FARGATE_DEFAULT_CPU_PERCENT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_FARGATE_DEFAULT_CPU_PERCENT: %q is not an integer: %w", v, err)
		}
		cfg.Runtime.Fargate.DefaultCPUPercent = n
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_DEFAULT_REPLICAS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_DEFAULT_REPLICAS: %q is not an integer: %w", v, err)
		}
		if n > 0 {
			cfg.Runtime.DefaultReplicas = n
		}
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_MAX_REPLICAS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_MAX_REPLICAS: %q is not an integer: %w", v, err)
		}
		if n > 0 {
			cfg.Runtime.MaxReplicas = n
		}
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_DEFAULT_MAX_SESSIONS_PER_REPLICA"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_DEFAULT_MAX_SESSIONS_PER_REPLICA: %q is not an integer: %w", v, err)
		}
		if n >= 0 {
			cfg.Runtime.DefaultMaxSessionsPerReplica = n
		}
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_DEFAULT_WORKER_ISOLATION"); v != "" {
		switch v {
		case string(IsolationMultiplex), string(IsolationGrouped), string(IsolationPerSession):
			cfg.Runtime.DefaultWorkerIsolation = v
		default:
			return fmt.Errorf("SHINYHUB_RUNTIME_DEFAULT_WORKER_ISOLATION: %q is not a valid mode", v)
		}
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_AUTOSCALE_ENABLED"); v != "" {
		b, err := parseBoolEnv(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_AUTOSCALE_ENABLED: %w", err)
		}
		cfg.Runtime.Autoscale.Enabled = b
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_AUTOSCALE_SCAN_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_AUTOSCALE_SCAN_INTERVAL: %q is not a duration: %w", v, err)
		}
		if d <= 0 {
			return fmt.Errorf("SHINYHUB_RUNTIME_AUTOSCALE_SCAN_INTERVAL: must be > 0, got %v", d)
		}
		cfg.Runtime.Autoscale.ScanInterval = d
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_AUTOSCALE_COOLDOWN"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_AUTOSCALE_COOLDOWN: %q is not a duration: %w", v, err)
		}
		if d <= 0 {
			return fmt.Errorf("SHINYHUB_RUNTIME_AUTOSCALE_COOLDOWN: must be > 0, got %v", d)
		}
		cfg.Runtime.Autoscale.Cooldown = d
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_AUTOSCALE_DEFAULT_TARGET"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("SHINYHUB_RUNTIME_AUTOSCALE_DEFAULT_TARGET: %q is not a number: %w", v, err)
		}
		if f <= 0 || f > 1 {
			return fmt.Errorf("SHINYHUB_RUNTIME_AUTOSCALE_DEFAULT_TARGET: must be in (0,1], got %v", f)
		}
		cfg.Runtime.Autoscale.DefaultTarget = f
	}
	if v := os.Getenv("SHINYHUB_DEFAULTS_APP_VISIBILITY"); v != "" {
		cfg.Defaults.AppVisibility = v
	}
	if v := os.Getenv("SHINYHUB_TRACING_ENABLED"); v != "" {
		b, err := parseBoolEnv(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_TRACING_ENABLED: %w", err)
		}
		cfg.Tracing.Enabled = b
	}
	if v := os.Getenv("SHINYHUB_TRACING_AUTO_INSTRUMENT_APPS"); v != "" {
		b, err := parseBoolEnv(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_TRACING_AUTO_INSTRUMENT_APPS: %w", err)
		}
		cfg.Tracing.AutoInstrumentApps = b
	}
	if v := os.Getenv("SHINYHUB_METRICS_ENABLED"); v != "" {
		b, err := parseBoolEnv(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_METRICS_ENABLED: %w", err)
		}
		cfg.Metrics.Enabled = b
	}
	if v := os.Getenv("SHINYHUB_METRICS_ADDR"); v != "" {
		cfg.Metrics.Addr = v
	}
	if v := os.Getenv("SHINYHUB_METRICS_HISTORY_WINDOW"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_METRICS_HISTORY_WINDOW: %q is not a duration: %w", v, err)
		}
		cfg.Metrics.HistoryWindow = d
	}
	if v := os.Getenv("SHINYHUB_METRICS_HISTORY_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_METRICS_HISTORY_INTERVAL: %q is not a duration: %w", v, err)
		}
		cfg.Metrics.HistoryInterval = d
	}
	if v := os.Getenv("SHINYHUB_TRACING_OTLP_ENDPOINT"); v != "" {
		cfg.Tracing.OTLPEndpoint = v
	}
	if v := os.Getenv("SHINYHUB_TRACING_OTLP_PROTOCOL"); v != "" {
		cfg.Tracing.OTLPProtocol = v
	}
	if v := os.Getenv("SHINYHUB_TRACING_OTLP_HEADERS"); v != "" {
		cfg.Tracing.OTLPHeaders = v
	}
	if v := os.Getenv("SHINYHUB_TRACING_SAMPLE_RATIO"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("SHINYHUB_TRACING_SAMPLE_RATIO: %q is not a number: %w", v, err)
		}
		cfg.Tracing.SampleRatio = f
	}
	if v := os.Getenv("SHINYHUB_TRACING_SLOW_REQUEST_MS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_TRACING_SLOW_REQUEST_MS: %q is not an integer: %w", v, err)
		}
		cfg.Tracing.SlowRequestMS = n
	}
	if v := os.Getenv("SHINYHUB_TRACING_RING_BUFFER_SIZE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_TRACING_RING_BUFFER_SIZE: %q is not an integer: %w", v, err)
		}
		cfg.Tracing.RingBufferSize = n
	}
	if v := os.Getenv("SHINYHUB_TRACING_TRACE_LINK_TEMPLATE"); v != "" {
		cfg.Tracing.TraceLinkTemplate = v
	}
	if v := os.Getenv("SHINYHUB_SCHEDULER_TIMEZONE"); v != "" {
		cfg.Scheduler.DefaultTimezone = v
	}
	if v := os.Getenv("SHINYHUB_BRANDING_SITE_TITLE"); v != "" {
		cfg.Branding.SiteTitle = v
	}
	if v := os.Getenv("SHINYHUB_BRANDING_ASSETS_DIR"); v != "" {
		cfg.Branding.AssetsDir = v
	}
	if v := os.Getenv("SHINYHUB_BRANDING_LOGO"); v != "" {
		cfg.Branding.Logo = v
	}
	if v := os.Getenv("SHINYHUB_BRANDING_FAVICON"); v != "" {
		cfg.Branding.Favicon = v
	}
	if v := os.Getenv("SHINYHUB_BRANDING_PRIMARY_COLOR"); v != "" {
		cfg.Branding.Theme.PrimaryColor = v
	}
	if v := os.Getenv("SHINYHUB_BRANDING_LANDING_PAGE"); v != "" {
		cfg.Branding.LandingPage = v
	}
	if v := os.Getenv("SHINYHUB_BRANDING_ROOT_BEHAVIOR"); v != "" {
		cfg.Branding.RootBehavior = v
	}
	if v := os.Getenv("SHINYHUB_WORKER_ENABLED"); v != "" {
		b, err := parseBoolEnv(v)
		if err != nil {
			return fmt.Errorf("SHINYHUB_WORKER_ENABLED: %w", err)
		}
		cfg.Worker.Enabled = b
	}
	if v := os.Getenv("SHINYHUB_WORKER_JOIN_TOKEN_FILE"); v != "" {
		cfg.Worker.JoinTokenFile = v
	}
	if v := os.Getenv("SHINYHUB_WORKER_CA_DIR"); v != "" {
		cfg.Worker.CADir = v
	}
	if v := os.Getenv("SHINYHUB_WORKER_LISTEN_ADDR"); v != "" {
		cfg.Worker.ListenAddr = v
	}
	if v := os.Getenv("SHINYHUB_WORKER_ADVERTISE_HOSTS"); v != "" {
		var hosts []string
		for _, h := range strings.Split(v, ",") {
			if s := strings.TrimSpace(h); s != "" {
				hosts = append(hosts, s)
			}
		}
		cfg.Worker.AdvertiseHosts = hosts
	}
	return nil
}

// HostBudgetMB returns the total RAM budget (in MiB) for app worker processes.
// 0 means the host-capacity guard is disabled.
func (c *Config) HostBudgetMB() int { return c.Server.HostBudgetMB }
