package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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

// Config holds all parsed, ready-to-use configuration for ShinyHub.
type Config struct {
	Database         DatabaseConfig
	Server           ServerConfig
	Auth             AuthConfig
	Storage          StorageConfig
	Lifecycle        LifecycleConfig
	Runtime          RuntimeConfig
	OAuth            OAuthConfig  `yaml:"-"`
	TrustedProxyNets []*net.IPNet `yaml:"-"` // parsed from Server.TrustedProxies
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
}

type AuthConfig struct {
	Secret string `yaml:"secret"`
}

type StorageConfig struct {
	AppsDir          string `yaml:"apps_dir"`
	VersionRetention int    `yaml:"version_retention"`
}

// RuntimeConfig controls how app processes are started and isolated.
type RuntimeConfig struct {
	Mode   string // "native" (default) or "docker"
	Docker DockerRuntimeConfig
}

// DockerRuntimeConfig holds Docker-specific runtime settings.
type DockerRuntimeConfig struct {
	Socket            string
	Images            DockerImages
	DefaultMemoryMB   int // 0 = no limit
	DefaultCPUPercent int // 0 = no limit; 100 = 1 full core
}

// DockerImages holds the base image names for each app type.
type DockerImages struct {
	Python string
	R      string
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
}

type rawLifecycleConfig struct {
	WatchInterval      string `yaml:"watch_interval"`
	RestartMaxAttempts int    `yaml:"restart_max_attempts"`
	HibernateTimeout   string `yaml:"hibernate_timeout"`
}

type rawRuntimeConfig struct {
	Mode   string               `yaml:"mode"`
	Docker rawDockerRuntimeConfig `yaml:"docker"`
}

type rawDockerRuntimeConfig struct {
	Socket            string          `yaml:"socket"`
	Images            rawDockerImages `yaml:"images"`
	DefaultMemoryMB   int             `yaml:"default_memory_mb"`
	DefaultCPUPercent int             `yaml:"default_cpu_percent"`
}

type rawDockerImages struct {
	Python string `yaml:"python"`
	R      string `yaml:"r"`
}

func Load(path string) (*Config, error) {
	raw := &rawConfig{
		Database: DatabaseConfig{Driver: "sqlite", DSN: "./data/shinyhub.db"},
		Server:   ServerConfig{Host: "0.0.0.0", Port: 8080},
		Storage:  StorageConfig{AppsDir: "./data/apps"},
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

	rc := parseRuntime(raw.Runtime)

	cfg := &Config{
		Database:  raw.Database,
		Server:    raw.Server,
		Auth:      raw.Auth,
		Storage:   raw.Storage,
		Lifecycle: lc,
		Runtime:   rc,
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
	applyEnv(cfg)

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

	if cfg.OAuth.OIDC.DisplayName == "" && cfg.OAuth.OIDC.IssuerURL != "" {
		cfg.OAuth.OIDC.DisplayName = "Sign in with SSO"
	}
	if cfg.Storage.VersionRetention <= 0 {
		cfg.Storage.VersionRetention = 5
	}
	if cfg.Auth.Secret == "" {
		return nil, fmt.Errorf("auth.secret must be set (SHINYHUB_AUTH_SECRET)")
	}
	return cfg, nil
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

func parseRuntime(r rawRuntimeConfig) RuntimeConfig {
	rc := RuntimeConfig{
		Mode: "native",
		Docker: DockerRuntimeConfig{
			Socket: "/var/run/docker.sock",
			Images: DockerImages{
				Python: "ghcr.io/astral-sh/uv:python3.12-bookworm-slim",
				R:      "rocker/r-base",
			},
		},
	}
	if r.Mode != "" {
		rc.Mode = r.Mode
	}
	if r.Docker.Socket != "" {
		rc.Docker.Socket = r.Docker.Socket
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
	return rc
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("SHINYHUB_AUTH_SECRET"); v != "" {
		cfg.Auth.Secret = v
	}
	if v := os.Getenv("SHINYHUB_DB_DSN"); v != "" {
		cfg.Database.DSN = v
	}
	if v := os.Getenv("SHINYHUB_APPS_DIR"); v != "" {
		cfg.Storage.AppsDir = v
	}
	if v := os.Getenv("SHINYHUB_STORAGE_VERSION_RETENTION"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Storage.VersionRetention = n
		}
	}
	if v := os.Getenv("SHINYHUB_BASE_URL"); v != "" {
		cfg.Server.BaseURL = v
	}
	if v := os.Getenv("SHINYHUB_TRUSTED_PROXIES"); v != "" {
		cfg.Server.TrustedProxies = strings.Split(v, ",")
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
	if v := os.Getenv("SHINYHUB_RUNTIME_DOCKER_DEFAULT_MEMORY_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Runtime.Docker.DefaultMemoryMB = n
		}
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_DOCKER_DEFAULT_CPU_PERCENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Runtime.Docker.DefaultCPUPercent = n
		}
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_DOCKER_IMAGE_PYTHON"); v != "" {
		cfg.Runtime.Docker.Images.Python = v
	}
	if v := os.Getenv("SHINYHUB_RUNTIME_DOCKER_IMAGE_R"); v != "" {
		cfg.Runtime.Docker.Images.R = v
	}
}
