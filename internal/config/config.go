package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// OAuthConfig holds OAuth2 provider credentials.
type OAuthConfig struct {
	GitHub GitHubOAuthConfig
	Google GoogleOAuthConfig
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
	Database  DatabaseConfig
	Server    ServerConfig
	Auth      AuthConfig
	Storage   StorageConfig
	Lifecycle LifecycleConfig
	OAuth     OAuthConfig `yaml:"-"`
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
	Host    string `yaml:"host"`
	Port    int    `yaml:"port"`
	BaseURL string `yaml:"base_url"`
}

type AuthConfig struct {
	Secret string `yaml:"secret"`
}

type StorageConfig struct {
	AppsDir string `yaml:"apps_dir"`
}

// rawConfig mirrors Config for YAML decoding, using string-typed duration fields.
type rawConfig struct {
	Database  DatabaseConfig     `yaml:"database"`
	Server    ServerConfig       `yaml:"server"`
	Auth      AuthConfig         `yaml:"auth"`
	Storage   StorageConfig      `yaml:"storage"`
	Lifecycle rawLifecycleConfig `yaml:"lifecycle"`
	OAuth     rawOAuthConfig     `yaml:"oauth"`
}

type rawLifecycleConfig struct {
	WatchInterval      string `yaml:"watch_interval"`
	RestartMaxAttempts int    `yaml:"restart_max_attempts"`
	HibernateTimeout   string `yaml:"hibernate_timeout"`
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

	cfg := &Config{
		Database:  raw.Database,
		Server:    raw.Server,
		Auth:      raw.Auth,
		Storage:   raw.Storage,
		Lifecycle: lc,
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
		},
	}
	applyEnv(cfg)
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
	if v := os.Getenv("SHINYHUB_BASE_URL"); v != "" {
		cfg.Server.BaseURL = v
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
}
