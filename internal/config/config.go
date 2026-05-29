package config

import (
	"fmt"
	"maps"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/rvben/shinyhub/internal/db"
)

// OAuthConfig holds OAuth2 provider credentials.
type OAuthConfig struct {
	GitHub GitHubOAuthConfig
	Google GoogleOAuthConfig
	OIDC   OIDCConfig
}

// OIDCConfig holds generic OpenID Connect provider credentials and metadata.
type OIDCConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	CallbackURL  string
	DisplayName  string // e.g. "Sign in with Okta"
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
	IssuerURL    string `yaml:"issuer_url"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	CallbackURL  string `yaml:"callback_url"`
	DisplayName  string `yaml:"display_name"`
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
}

// BrandingConfig customises the ShinyHub front door. Every field is optional;
// the zero value behaves as if no branding is configured.
type BrandingConfig struct {
	SiteTitle   string       `yaml:"site_title"`
	AssetsDir   string       `yaml:"assets_dir"`
	Logo        string       `yaml:"logo"`
	Favicon     string       `yaml:"favicon"`
	LandingPage string       `yaml:"landing_page"`
	Theme       ThemeConfig  `yaml:"theme"`
	FooterLinks []FooterLink `yaml:"footer_links"`

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
}

type AuthConfig struct {
	Secret string `yaml:"secret"`
	// OAuthDefaultRole is the role assigned to users created via just-in-time
	// provisioning during OAuth/OIDC sign-in (i.e. first-time login). Allowed
	// values: "viewer" (default), "developer", "operator". "admin" is
	// intentionally not permitted — admin must be granted explicitly, never
	// auto-provisioned from an external IdP.
	OAuthDefaultRole string `yaml:"oauth_default_role"`

	// DeployToken is a pre-shared bearer token sourced from
	// SHINYHUB_DEPLOY_TOKEN. When non-empty it authenticates as the synthetic
	// system user `__deploy__` with role DeployTokenRole. Not persisted; rotation
	// is "change the env var, restart the service."
	DeployToken string `yaml:"-"`

	// DeployTokenRole is the role granted to the synthetic system user when the
	// env-token is active. Sourced from SHINYHUB_DEPLOY_TOKEN_ROLE; default
	// "developer". Must be one of viewer, developer, operator, admin.
	DeployTokenRole string `yaml:"-"`
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
	DefaultReplicas int
	MaxReplicas     int
	// DefaultMaxSessionsPerReplica is the fallback session cap enforced by the
	// proxy when an app's own max_sessions_per_replica is 0. Once every replica
	// reaches this many active connections, new cookie-less requests are shed
	// with 503. 0 here disables the cap entirely (unlimited).
	DefaultMaxSessionsPerReplica int
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
}

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
	Name    string
	Runtime string
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
	Database  DatabaseConfig     `yaml:"database"`
	Server    ServerConfig       `yaml:"server"`
	Auth      AuthConfig         `yaml:"auth"`
	Storage   StorageConfig      `yaml:"storage"`
	Lifecycle rawLifecycleConfig `yaml:"lifecycle"`
	OAuth     rawOAuthConfig     `yaml:"oauth"`
	Runtime   rawRuntimeConfig   `yaml:"runtime"`
	Scheduler rawSchedulerConfig `yaml:"scheduler"`
	Defaults  rawDefaultsConfig  `yaml:"defaults"`
	Tracing   rawTracingConfig   `yaml:"tracing"`
	Metrics   rawMetricsConfig   `yaml:"metrics"`
	Branding  BrandingConfig     `yaml:"branding"`
	Worker    WorkerConfig       `yaml:"worker"`
}

type rawMetricsConfig struct {
	Enabled bool   `yaml:"enabled"`
	Addr    string `yaml:"addr"`
}

type rawTracingConfig struct {
	Enabled      bool   `yaml:"enabled"`
	OTLPEndpoint string `yaml:"otlp_endpoint"`
	OTLPProtocol string `yaml:"otlp_protocol"`
	OTLPHeaders  string `yaml:"otlp_headers"`
	// Pointers so an explicit 0 (a documented, meaningful value: 0 disables
	// sampling / disables the ring buffer / retains only error spans) is
	// distinguishable from the key being absent (apply the safe default).
	SampleRatio       *float64 `yaml:"sample_ratio"`
	SlowRequestMS     *int     `yaml:"slow_request_ms"`
	RingBufferSize    *int     `yaml:"ring_buffer_size"`
	TraceLinkTemplate string   `yaml:"trace_link_template"`
}

type rawLifecycleConfig struct {
	WatchInterval      string `yaml:"watch_interval"`
	RestartMaxAttempts int    `yaml:"restart_max_attempts"`
	HibernateTimeout   string `yaml:"hibernate_timeout"`
}

type rawRuntimeConfig struct {
	Mode            string                 `yaml:"mode"`
	Docker          rawDockerRuntimeConfig `yaml:"docker"`
	DefaultReplicas int                    `yaml:"default_replicas"`
	MaxReplicas     int                    `yaml:"max_replicas"`
	// Pointer so an explicit 0 (documented as "unlimited") is
	// distinguishable from the key being absent (apply the safe default).
	DefaultMaxSessionsPerReplica *int                    `yaml:"default_max_sessions_per_replica"`
	Tiers                        []rawTierConfig         `yaml:"tiers"`
	Autoscale                    rawAutoscaleConfig      `yaml:"autoscale"`
	Fargate                      rawFargateRuntimeConfig `yaml:"fargate"`
}

type rawFargateRuntimeConfig struct {
	Cluster          string   `yaml:"cluster"`
	TaskDefinition   string   `yaml:"task_definition"`
	ContainerName    string   `yaml:"container_name"`
	Subnets          []string `yaml:"subnets"`
	SecurityGroups   []string `yaml:"security_groups"`
	AssignPublicIP   bool     `yaml:"assign_public_ip"`
	PlatformVersion  string   `yaml:"platform_version"`
	Region           string   `yaml:"region"`
	RouteViaPublicIP bool     `yaml:"route_via_public_ip"`
}

type rawAutoscaleConfig struct {
	Enabled      bool   `yaml:"enabled"`
	ScanInterval string `yaml:"scan_interval"`
	Cooldown     string `yaml:"cooldown"`
	// Pointer so an explicit 0 is distinguishable from the key being absent.
	DefaultTarget *float64 `yaml:"default_target"`
}

type rawTierConfig struct {
	Name    string `yaml:"name"`
	Runtime string `yaml:"runtime"`
}

type rawDockerRuntimeConfig struct {
	Socket            string          `yaml:"socket"`
	Images            rawDockerImages `yaml:"images"`
	DefaultMemoryMB   int             `yaml:"default_memory_mb"`
	DefaultCPUPercent int             `yaml:"default_cpu_percent"`
	NetworkMode       string          `yaml:"network_mode"`
}

type rawDockerImages struct {
	Python string `yaml:"python"`
	R      string `yaml:"r"`
}

func Load(path string) (*Config, error) {
	raw := &rawConfig{
		Database: DatabaseConfig{Driver: "sqlite", DSN: "./data/shinyhub.db"},
		Server:   ServerConfig{Host: "0.0.0.0", Port: 8080},
		Storage:  StorageConfig{AppsDir: "./data/apps", AppDataDir: "./data/app-data", MaxBundleMB: 128},
	}
	if path != "" {
		f, err := os.Open(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("open config: %w", err)
		}
		if err == nil {
			defer f.Close()
			if err := yaml.NewDecoder(f).Decode(raw); err != nil {
				return nil, fmt.Errorf("parse config: %w", err)
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
			SampleRatio:       derefOr(raw.Tracing.SampleRatio, 0.1),
			SlowRequestMS:     derefOr(raw.Tracing.SlowRequestMS, 1000),
			RingBufferSize:    derefOr(raw.Tracing.RingBufferSize, 200),
			TraceLinkTemplate: raw.Tracing.TraceLinkTemplate,
		},
		Metrics: MetricsConfig{
			Enabled: raw.Metrics.Enabled,
			Addr:    raw.Metrics.Addr,
		},
		Branding: raw.Branding,
		Worker:   raw.Worker,
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
				IssuerURL:    raw.OAuth.OIDC.IssuerURL,
				ClientID:     raw.OAuth.OIDC.ClientID,
				ClientSecret: raw.OAuth.OIDC.ClientSecret,
				CallbackURL:  raw.OAuth.OIDC.CallbackURL,
				DisplayName:  raw.OAuth.OIDC.DisplayName,
			},
		},
	}
	if err := applyEnv(cfg); err != nil {
		return nil, err
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

	if cfg.OAuth.OIDC.DisplayName == "" && cfg.OAuth.OIDC.IssuerURL != "" {
		cfg.OAuth.OIDC.DisplayName = "Sign in with SSO"
	}
	if cfg.Storage.VersionRetention <= 0 {
		cfg.Storage.VersionRetention = 5
	}
	if cfg.Storage.AppQuotaMB < 0 {
		cfg.Storage.AppQuotaMB = 0
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
			cfg.Runtime.Tiers = append(cfg.Runtime.Tiers, TierConfig{Name: t.Name, Runtime: t.Runtime})
		}
		if hasFargate {
			if err := validateFargate(cfg.Runtime.Fargate); err != nil {
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
	if cfg.Auth.Secret == "" {
		return nil, fmt.Errorf("auth.secret must be set (SHINYHUB_AUTH_SECRET)")
	}
	if cfg.Auth.Secret == "change-me-to-a-random-string" {
		return nil, fmt.Errorf("auth.secret is the placeholder value from shinyhub.yaml.example; generate a strong value with: openssl rand -hex 32")
	}
	if len(cfg.Auth.Secret) < 32 {
		return nil, fmt.Errorf("auth.secret must be at least 32 characters (got %d); generate one with: openssl rand -hex 32", len(cfg.Auth.Secret))
	}
	if err := validateBranding(&cfg.Branding); err != nil {
		return nil, err
	}
	return cfg, nil
}

// normalizeTracing applies defaults and validates the tracing block. When
// Enabled is false the block is left mostly untouched so disabled config still
// round-trips through Load (callers should not read fields without checking
// Enabled).
func normalizeTracing(t *TracingConfig) error {
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

// validateFargate enforces the settings a fargate tier cannot run without. It is
// only called when at least one tier declares runtime "fargate", so a config
// with no fargate tier never needs the block populated.
func validateFargate(f FargateRuntimeConfig) error {
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
	}
	if r.Mode != "" {
		rc.Mode = r.Mode
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

	rc.Fargate = FargateRuntimeConfig{
		Cluster:          r.Fargate.Cluster,
		TaskDefinition:   r.Fargate.TaskDefinition,
		ContainerName:    r.Fargate.ContainerName,
		Subnets:          r.Fargate.Subnets,
		SecurityGroups:   r.Fargate.SecurityGroups,
		AssignPublicIP:   r.Fargate.AssignPublicIP,
		PlatformVersion:  r.Fargate.PlatformVersion,
		Region:           r.Fargate.Region,
		RouteViaPublicIP: r.Fargate.RouteViaPublicIP,
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
	if v := os.Getenv("SHINYHUB_DB_DSN"); v != "" {
		cfg.Database.DSN = v
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
	if v := os.Getenv("SHINYHUB_TRUSTED_PROXIES"); v != "" {
		cfg.Server.TrustedProxies = strings.Split(v, ",")
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
	if v := os.Getenv("SHINYHUB_RUNTIME_MODE"); v != "" {
		cfg.Runtime.Mode = v
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
