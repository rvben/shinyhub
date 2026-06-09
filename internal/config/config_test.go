package config_test

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
)

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	f.Close()
	return f.Name()
}

func TestLifecycle_Defaults(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Lifecycle.WatchInterval != 15*time.Second {
		t.Errorf("WatchInterval default: got %v, want 15s", cfg.Lifecycle.WatchInterval)
	}
	if cfg.Lifecycle.RestartMaxAttempts != 5 {
		t.Errorf("RestartMaxAttempts default: got %d, want 5", cfg.Lifecycle.RestartMaxAttempts)
	}
	if cfg.Lifecycle.HibernateTimeout != 30*time.Minute {
		t.Errorf("HibernateTimeout default: got %v, want 30m", cfg.Lifecycle.HibernateTimeout)
	}
}

func TestServerShutdownApps(t *testing.T) {
	t.Run("defaults to adopt", func(t *testing.T) {
		t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
		cfg, err := config.Load("")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Server.ShutdownApps != "adopt" {
			t.Errorf("default: got %q, want adopt", cfg.Server.ShutdownApps)
		}
	})

	t.Run("accepts stop from yaml", func(t *testing.T) {
		path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
server:
  shutdown_apps: stop
`)
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Server.ShutdownApps != "stop" {
			t.Errorf("got %q, want stop", cfg.Server.ShutdownApps)
		}
	})

	t.Run("rejects invalid value", func(t *testing.T) {
		path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
server:
  shutdown_apps: nuke
`)
		if _, err := config.Load(path); err == nil {
			t.Fatal("expected error for invalid shutdown_apps, got nil")
		}
	})

	t.Run("env override wins", func(t *testing.T) {
		t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
		t.Setenv("SHINYHUB_SHUTDOWN_APPS", "stop")
		cfg, err := config.Load("")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Server.ShutdownApps != "stop" {
			t.Errorf("env override: got %q, want stop", cfg.Server.ShutdownApps)
		}
	})
}

func TestLifecycle_FromYAML(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
lifecycle:
  watch_interval: 30s
  restart_max_attempts: 3
  hibernate_timeout: 10m
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Lifecycle.WatchInterval != 30*time.Second {
		t.Errorf("WatchInterval: got %v, want 30s", cfg.Lifecycle.WatchInterval)
	}
	if cfg.Lifecycle.RestartMaxAttempts != 3 {
		t.Errorf("RestartMaxAttempts: got %d, want 3", cfg.Lifecycle.RestartMaxAttempts)
	}
	if cfg.Lifecycle.HibernateTimeout != 10*time.Minute {
		t.Errorf("HibernateTimeout: got %v, want 10m", cfg.Lifecycle.HibernateTimeout)
	}
}

func TestLifecycle_HibernateDisabled(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
lifecycle:
  hibernate_timeout: 0s
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Lifecycle.HibernateTimeout != 0 {
		t.Errorf("expected 0 (disabled globally), got %v", cfg.Lifecycle.HibernateTimeout)
	}
}

func TestLifecycle_InvalidDuration(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
lifecycle:
  watch_interval: "not-a-duration"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Error("expected error for invalid duration, got nil")
	}
}

func TestTrustedProxies_Default(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.TrustedProxyNets) == 0 {
		t.Fatal("expected default trusted proxy nets, got none")
	}
	// 127.0.0.1 must be trusted by default.
	found := false
	for _, n := range cfg.TrustedProxyNets {
		if n.Contains(parseIP(t, "127.0.0.1")) {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 127.0.0.1 to be in default trusted proxies")
	}
}

func TestTrustedProxies_FromYAML(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
server:
  trusted_proxies:
    - "10.0.0.0/8"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.TrustedProxyNets) != 1 {
		t.Fatalf("expected 1 trusted net, got %d", len(cfg.TrustedProxyNets))
	}
	if !cfg.TrustedProxyNets[0].Contains(parseIP(t, "10.1.2.3")) {
		t.Error("expected 10.1.2.3 to be in 10.0.0.0/8")
	}
	if cfg.TrustedProxyNets[0].Contains(parseIP(t, "192.168.1.1")) {
		t.Error("expected 192.168.1.1 NOT to be in 10.0.0.0/8")
	}
}

func TestTrustedProxies_InvalidCIDR(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
server:
  trusted_proxies:
    - "not-a-cidr"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Error("expected error for invalid CIDR, got nil")
	}
}

func parseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("invalid IP %q", s)
	}
	return ip
}

func TestConfig_GoogleOAuth_EnvVars(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_GOOGLE_CLIENT_ID", "g-client-id")
	t.Setenv("SHINYHUB_GOOGLE_CLIENT_SECRET", "g-client-secret")
	t.Setenv("SHINYHUB_GOOGLE_CALLBACK_URL", "http://localhost/google/callback")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OAuth.Google.ClientID != "g-client-id" {
		t.Errorf("ClientID = %q, want %q", cfg.OAuth.Google.ClientID, "g-client-id")
	}
	if cfg.OAuth.Google.ClientSecret != "g-client-secret" {
		t.Errorf("ClientSecret = %q, want %q", cfg.OAuth.Google.ClientSecret, "g-client-secret")
	}
	if cfg.OAuth.Google.CallbackURL != "http://localhost/google/callback" {
		t.Errorf("CallbackURL = %q, want %q", cfg.OAuth.Google.CallbackURL, "http://localhost/google/callback")
	}
}

func TestRuntimeConfig(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_RUNTIME_MODE", "docker")
	t.Setenv("SHINYHUB_RUNTIME_DOCKER_SOCKET", "/run/docker.sock")
	t.Setenv("SHINYHUB_RUNTIME_DOCKER_DEFAULT_MEMORY_MB", "512")
	t.Setenv("SHINYHUB_RUNTIME_DOCKER_DEFAULT_CPU_PERCENT", "100")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Mode != "docker" {
		t.Errorf("expected mode docker, got %s", cfg.Runtime.Mode)
	}
	if cfg.Runtime.Docker.Socket != "/run/docker.sock" {
		t.Errorf("unexpected socket: %s", cfg.Runtime.Docker.Socket)
	}
	if cfg.Runtime.Docker.DefaultMemoryMB != 512 {
		t.Errorf("expected 512, got %d", cfg.Runtime.Docker.DefaultMemoryMB)
	}
	if cfg.Runtime.Docker.DefaultCPUPercent != 100 {
		t.Errorf("expected 100, got %d", cfg.Runtime.Docker.DefaultCPUPercent)
	}
}

func TestRuntimeConfigDefaults(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Mode != "native" {
		t.Errorf("expected default mode native, got %s", cfg.Runtime.Mode)
	}
	if cfg.Runtime.Docker.Socket != "/var/run/docker.sock" {
		t.Errorf("expected default socket, got %s", cfg.Runtime.Docker.Socket)
	}
	if cfg.Runtime.Docker.Images.Python == "" {
		t.Error("expected non-empty default python image")
	}
	if cfg.Runtime.Docker.Images.R == "" {
		t.Error("expected non-empty default R image")
	}
}

func TestDefaultsAppVisibility_Default(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Defaults.AppVisibility != "private" {
		t.Errorf("default AppVisibility: got %q, want %q", cfg.Defaults.AppVisibility, "private")
	}
}

func TestDefaultsAppVisibility_FromYAML(t *testing.T) {
	for _, vis := range []string{"private", "shared", "public"} {
		t.Run(vis, func(t *testing.T) {
			path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
defaults:
  app_visibility: `+vis+`
`)
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Defaults.AppVisibility != vis {
				t.Errorf("got %q, want %q", cfg.Defaults.AppVisibility, vis)
			}
		})
	}
}

func TestDefaultsAppVisibility_FromEnv(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_DEFAULTS_APP_VISIBILITY", "public")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Defaults.AppVisibility != "public" {
		t.Errorf("got %q, want %q", cfg.Defaults.AppVisibility, "public")
	}
}

func TestDefaultsAppVisibility_EnvOverridesYAML(t *testing.T) {
	t.Setenv("SHINYHUB_DEFAULTS_APP_VISIBILITY", "shared")
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
defaults:
  app_visibility: public
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Defaults.AppVisibility != "shared" {
		t.Errorf("env should override YAML: got %q, want %q", cfg.Defaults.AppVisibility, "shared")
	}
}

func TestDefaultsAppVisibility_InvalidValue(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
defaults:
  app_visibility: secret
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for invalid app_visibility, got nil")
	}
	if !strings.Contains(err.Error(), "defaults.app_visibility") {
		t.Errorf("error should mention defaults.app_visibility: %v", err)
	}
	if !strings.Contains(err.Error(), "private") || !strings.Contains(err.Error(), "shared") || !strings.Contains(err.Error(), "public") {
		t.Errorf("error should mention valid values: %v", err)
	}
}

func TestRuntimeConfigImageEnvOverrides(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_RUNTIME_DOCKER_IMAGE_PYTHON", "my-registry/uv:custom")
	t.Setenv("SHINYHUB_RUNTIME_DOCKER_IMAGE_R", "my-registry/r-base:custom")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Docker.Images.Python != "my-registry/uv:custom" {
		t.Errorf("expected custom python image, got %s", cfg.Runtime.Docker.Images.Python)
	}
	if cfg.Runtime.Docker.Images.R != "my-registry/r-base:custom" {
		t.Errorf("expected custom R image, got %s", cfg.Runtime.Docker.Images.R)
	}
}

func TestLoad_RejectsPlaceholderSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("auth:\n  secret: change-me-to-a-random-string\n"), 0600); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("SHINYHUB_AUTH_SECRET")
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for placeholder secret, got nil")
	}
	if !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("expected placeholder error, got %v", err)
	}
}

func TestLoad_RejectsShortSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("auth:\n  secret: short\n"), 0600); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("SHINYHUB_AUTH_SECRET")
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for short secret, got nil")
	}
	if !strings.Contains(err.Error(), "32") {
		t.Fatalf("expected length error mentioning 32, got %v", err)
	}
}

func TestStorage_AppQuotaMB_DefaultsToDisabled(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Storage.AppQuotaMB != 0 {
		t.Errorf("AppQuotaMB default: got %d, want 0 (disabled)", cfg.Storage.AppQuotaMB)
	}
}

func TestStorage_AppQuotaMB_FromYAML(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
storage:
  app_quota_mb: 512
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Storage.AppQuotaMB != 512 {
		t.Errorf("AppQuotaMB: got %d, want 512", cfg.Storage.AppQuotaMB)
	}
}

func TestStorage_AppQuotaMB_NegativeNormalizesToZero(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
storage:
  app_quota_mb: -1
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Storage.AppQuotaMB != 0 {
		t.Errorf("expected negative to normalize to 0 (disabled), got %d", cfg.Storage.AppQuotaMB)
	}
}

func TestStorage_AppQuotaMB_EnvOverride(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_APP_QUOTA_MB", "1024")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Storage.AppQuotaMB != 1024 {
		t.Errorf("AppQuotaMB from env: got %d, want 1024", cfg.Storage.AppQuotaMB)
	}
}

func TestConfig_RuntimeReplicaDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
runtime:
  default_replicas: 2
  max_replicas: 16
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtime.DefaultReplicas != 2 {
		t.Fatalf("DefaultReplicas: got %d, want 2", cfg.Runtime.DefaultReplicas)
	}
	if cfg.Runtime.MaxReplicas != 16 {
		t.Fatalf("MaxReplicas: got %d, want 16", cfg.Runtime.MaxReplicas)
	}
}

func TestConfig_RuntimeReplicaDefaultsFallback(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("") // empty path → all defaults
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtime.DefaultReplicas != 1 {
		t.Fatalf("default DefaultReplicas: got %d, want 1", cfg.Runtime.DefaultReplicas)
	}
	if cfg.Runtime.MaxReplicas != 32 {
		t.Fatalf("default MaxReplicas: got %d, want 32", cfg.Runtime.MaxReplicas)
	}
}

func TestConfig_RuntimeReplicaEnvOverride(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_RUNTIME_DEFAULT_REPLICAS", "4")
	t.Setenv("SHINYHUB_RUNTIME_MAX_REPLICAS", "24")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtime.DefaultReplicas != 4 {
		t.Fatalf("env DefaultReplicas: got %d, want 4", cfg.Runtime.DefaultReplicas)
	}
	if cfg.Runtime.MaxReplicas != 24 {
		t.Fatalf("env MaxReplicas: got %d, want 24", cfg.Runtime.MaxReplicas)
	}
}

func TestConfig_AutoscaleDefaults(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("") // empty path → all defaults
	if err != nil {
		t.Fatal(err)
	}
	as := cfg.Runtime.Autoscale
	if as.Enabled {
		t.Fatalf("default Autoscale.Enabled = true, want false")
	}
	if as.ScanInterval != 30*time.Second {
		t.Fatalf("default ScanInterval = %v, want 30s", as.ScanInterval)
	}
	if as.Cooldown != 3*time.Minute {
		t.Fatalf("default Cooldown = %v, want 3m", as.Cooldown)
	}
	if as.DefaultTarget != 0.8 {
		t.Fatalf("default DefaultTarget = %v, want 0.8", as.DefaultTarget)
	}
}

func TestConfig_AutoscaleFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
runtime:
  autoscale:
    enabled: true
    scan_interval: 10s
    cooldown: 1m
    default_target: 0.6
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	as := cfg.Runtime.Autoscale
	if !as.Enabled {
		t.Fatalf("Autoscale.Enabled = false, want true")
	}
	if as.ScanInterval != 10*time.Second {
		t.Fatalf("ScanInterval = %v, want 10s", as.ScanInterval)
	}
	if as.Cooldown != time.Minute {
		t.Fatalf("Cooldown = %v, want 1m", as.Cooldown)
	}
	if as.DefaultTarget != 0.6 {
		t.Fatalf("DefaultTarget = %v, want 0.6", as.DefaultTarget)
	}
}

func TestConfig_AutoscaleInvalidIntervalRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
runtime:
  autoscale:
    scan_interval: "not-a-duration"
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatalf("Load accepted invalid scan_interval, want error")
	}
}

func TestConfig_AutoscaleNonPositiveIntervalRejected(t *testing.T) {
	// A zero or negative scan interval would panic time.NewTicker when the
	// controller starts, so it must be rejected at load time.
	for _, val := range []string{"0s", "-5s"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(`
runtime:
  autoscale:
    scan_interval: "`+val+`"
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
`), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := config.Load(path); err == nil {
			t.Fatalf("Load accepted non-positive scan_interval %q, want error", val)
		}
	}
}

func TestConfig_AutoscaleNonPositiveCooldownRejected(t *testing.T) {
	// A zero or negative cooldown disables the gate that prevents the controller
	// from acting every tick, so it must be rejected at load time.
	for _, val := range []string{"0s", "-1m"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(`
runtime:
  autoscale:
    cooldown: "`+val+`"
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
`), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := config.Load(path); err == nil {
			t.Fatalf("Load accepted non-positive cooldown %q, want error", val)
		}
	}
}

func TestConfig_AutoscaleInvalidTargetRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
runtime:
  autoscale:
    default_target: 1.5
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatalf("Load accepted out-of-range default_target, want error")
	}
}

func TestLoad_AcceptsStrongSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	strong := strings.Repeat("a", 32)
	if err := os.WriteFile(path, []byte("auth:\n  secret: "+strong+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("SHINYHUB_AUTH_SECRET")
	_, err := config.Load(path)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestLoad_AppDataDirDefaults(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 32))
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Load normalizes storage roots to absolute, so the default "./data/app-data"
	// becomes an absolute path rooted at the process cwd.
	if !filepath.IsAbs(cfg.Storage.AppDataDir) {
		t.Errorf("AppDataDir default not absolute: %q", cfg.Storage.AppDataDir)
	}
	if !strings.HasSuffix(cfg.Storage.AppDataDir, "/data/app-data") {
		t.Errorf("AppDataDir default = %q, want suffix /data/app-data", cfg.Storage.AppDataDir)
	}
	if got, want := cfg.Storage.MaxBundleMB, 128; got != want {
		t.Errorf("MaxBundleMB default = %d, want %d", got, want)
	}
}

func TestLoad_AppDataDirEnvOverride(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 32))
	t.Setenv("SHINYHUB_APP_DATA_DIR", "/srv/shiny/data")
	t.Setenv("SHINYHUB_MAX_BUNDLE_MB", "256")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Storage.AppDataDir, "/srv/shiny/data"; got != want {
		t.Errorf("AppDataDir = %q, want %q", got, want)
	}
	if got, want := cfg.Storage.MaxBundleMB, 256; got != want {
		t.Errorf("MaxBundleMB = %d, want %d", got, want)
	}
}

func TestLoad_MaxBundleMBZeroMeansNoCap(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 32))
	t.Setenv("SHINYHUB_MAX_BUNDLE_MB", "0")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Storage.MaxBundleMB; got != 0 {
		t.Errorf("MaxBundleMB with explicit 0 = %d, want 0 (disables cap)", got)
	}
}

func TestAuth_OAuthDefaultRole_DefaultsToViewer(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 32))
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Auth.OAuthDefaultRole, "viewer"; got != want {
		t.Errorf("OAuthDefaultRole default = %q, want %q", got, want)
	}
}

func TestAuth_OAuthDefaultRole_FromYAML(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
  oauth_default_role: developer
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Auth.OAuthDefaultRole, "developer"; got != want {
		t.Errorf("OAuthDefaultRole = %q, want %q", got, want)
	}
}

func TestAuth_OAuthDefaultRole_EnvOverride(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 32))
	t.Setenv("SHINYHUB_AUTH_OAUTH_DEFAULT_ROLE", "operator")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Auth.OAuthDefaultRole, "operator"; got != want {
		t.Errorf("OAuthDefaultRole from env = %q, want %q", got, want)
	}
}

func TestAuth_OAuthDefaultRole_RejectsInvalidValue(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
  oauth_default_role: superuser
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for invalid oauth_default_role, got nil")
	}
	if !strings.Contains(err.Error(), "oauth_default_role") {
		t.Fatalf("expected error mentioning oauth_default_role, got %v", err)
	}
}

func TestAuth_OAuthDefaultRole_RejectsAdmin(t *testing.T) {
	// Admin must never be auto-granted via JIT provisioning — it's reserved
	// for the bootstrap admin and explicit promotions.
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
  oauth_default_role: admin
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error rejecting admin as JIT default, got nil")
	}
}

func TestRuntime_Docker_NetworkMode_DefaultsToBridge(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 32))
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Runtime.Docker.NetworkMode, "bridge"; got != want {
		t.Errorf("Runtime.Docker.NetworkMode default = %q, want %q", got, want)
	}
}

func TestRuntime_Docker_NetworkMode_AcceptsHost(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  docker:
    network_mode: host
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Runtime.Docker.NetworkMode, "host"; got != want {
		t.Errorf("Runtime.Docker.NetworkMode = %q, want %q", got, want)
	}
}

func TestRuntime_Docker_NetworkMode_RejectsUnknown(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  docker:
    network_mode: macvlan
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for unknown network_mode, got nil")
	}
	if !strings.Contains(err.Error(), "network_mode") {
		t.Fatalf("expected error mentioning network_mode, got %v", err)
	}
}

func TestRuntime_Docker_NetworkMode_EnvOverride(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 32))
	t.Setenv("SHINYHUB_RUNTIME_DOCKER_NETWORK_MODE", "host")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Runtime.Docker.NetworkMode, "host"; got != want {
		t.Errorf("Runtime.Docker.NetworkMode from env = %q, want %q", got, want)
	}
}

func TestRuntime_Mode_RejectsUnknownValue(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  mode: dockre
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for unknown runtime.mode, got nil")
	}
	if !strings.Contains(err.Error(), "runtime.mode") {
		t.Fatalf("expected error mentioning runtime.mode, got %v", err)
	}
	if !strings.Contains(err.Error(), "dockre") {
		t.Fatalf("expected error to surface the offending value, got %v", err)
	}
}

func TestRuntime_Mode_RejectsUnknownEnvValue(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 32))
	t.Setenv("SHINYHUB_RUNTIME_MODE", "kubernetes")
	_, err := config.Load("")
	if err == nil {
		t.Fatal("expected error for unknown SHINYHUB_RUNTIME_MODE, got nil")
	}
	if !strings.Contains(err.Error(), "runtime.mode") {
		t.Fatalf("expected error mentioning runtime.mode, got %v", err)
	}
}

func TestRuntime_Mode_AcceptsNative(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  mode: native
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Mode != "native" {
		t.Errorf("Runtime.Mode = %q, want native", cfg.Runtime.Mode)
	}
}

func TestRuntime_Mode_AcceptsDocker(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  mode: docker
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Mode != "docker" {
		t.Errorf("Runtime.Mode = %q, want docker", cfg.Runtime.Mode)
	}
}

func TestLoad_DeployTokenFromEnv(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 32))
	t.Setenv("SHINYHUB_DEPLOY_TOKEN", "shk_"+strings.Repeat("b", 64))
	t.Setenv("SHINYHUB_DEPLOY_TOKEN_ROLE", "operator")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.DeployToken != "shk_"+strings.Repeat("b", 64) {
		t.Errorf("DeployToken = %q, want shk_...", cfg.Auth.DeployToken)
	}
	if cfg.Auth.DeployTokenRole != "operator" {
		t.Errorf("DeployTokenRole = %q, want operator", cfg.Auth.DeployTokenRole)
	}
}

func TestLoad_DeployTokenRoleDefault(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 32))
	t.Setenv("SHINYHUB_DEPLOY_TOKEN", "shk_"+strings.Repeat("b", 64))

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.DeployTokenRole != "developer" {
		t.Errorf("DeployTokenRole = %q, want developer (default)", cfg.Auth.DeployTokenRole)
	}
}

func TestLoad_DeployTokenRoleInvalid(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 32))
	t.Setenv("SHINYHUB_DEPLOY_TOKEN", "shk_"+strings.Repeat("b", 64))
	t.Setenv("SHINYHUB_DEPLOY_TOKEN_ROLE", "godmode")

	_, err := config.Load("")
	if err == nil {
		t.Fatal("expected error for invalid role, got nil")
	}
	if !strings.Contains(err.Error(), "deploy_token_role") {
		t.Errorf("error %q should mention deploy_token_role", err)
	}
}

func TestLoad_MaxBundleMBNegativeNormalized(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
storage:
  max_bundle_mb: -5
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Storage.MaxBundleMB; got != 128 {
		t.Errorf("MaxBundleMB with negative input = %d, want 128 (default)", got)
	}
}

func TestTracing_DisabledByDefault(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tracing.Enabled {
		t.Errorf("Tracing.Enabled default = true, want false")
	}
}

func TestTracing_DefaultsAppliedWhenEnabled(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
tracing:
  enabled: true
  otlp_endpoint: http://collector:4318
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tracing.OTLPProtocol != "http/protobuf" {
		t.Errorf("OTLPProtocol default = %q, want http/protobuf", cfg.Tracing.OTLPProtocol)
	}
	if cfg.Tracing.SampleRatio != 0.1 {
		t.Errorf("SampleRatio default = %g, want 0.1", cfg.Tracing.SampleRatio)
	}
	if cfg.Tracing.SlowRequestMS != 1000 {
		t.Errorf("SlowRequestMS default = %d, want 1000", cfg.Tracing.SlowRequestMS)
	}
	if cfg.Tracing.RingBufferSize != 200 {
		t.Errorf("RingBufferSize default = %d, want 200", cfg.Tracing.RingBufferSize)
	}
}

func TestTracing_RejectsUnknownProtocol(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
tracing:
  enabled: true
  otlp_endpoint: http://collector:4318
  otlp_protocol: thrift
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for unknown otlp_protocol")
	}
	if !strings.Contains(err.Error(), "otlp_protocol") {
		t.Errorf("error should mention otlp_protocol: %v", err)
	}
}

func TestTracing_RejectsSampleRatioOutOfRange(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
tracing:
  enabled: true
  otlp_endpoint: http://collector:4318
  sample_ratio: 1.5
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for sample_ratio > 1")
	}
	if !strings.Contains(err.Error(), "sample_ratio") {
		t.Errorf("error should mention sample_ratio: %v", err)
	}
}

func TestTracing_EnvOverrides(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_TRACING_ENABLED", "true")
	t.Setenv("SHINYHUB_TRACING_OTLP_ENDPOINT", "http://env-collector:4318")
	t.Setenv("SHINYHUB_TRACING_OTLP_PROTOCOL", "grpc")
	t.Setenv("SHINYHUB_TRACING_OTLP_HEADERS", "x-token=secret")
	t.Setenv("SHINYHUB_TRACING_SAMPLE_RATIO", "0.5")
	t.Setenv("SHINYHUB_TRACING_SLOW_REQUEST_MS", "250")
	t.Setenv("SHINYHUB_TRACING_RING_BUFFER_SIZE", "50")
	t.Setenv("SHINYHUB_TRACING_TRACE_LINK_TEMPLATE", "https://tempo.example/{trace_id}")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Tracing.Enabled {
		t.Errorf("env-driven Enabled not applied")
	}
	if cfg.Tracing.OTLPEndpoint != "http://env-collector:4318" {
		t.Errorf("OTLPEndpoint = %q", cfg.Tracing.OTLPEndpoint)
	}
	if cfg.Tracing.OTLPProtocol != "grpc" {
		t.Errorf("OTLPProtocol = %q", cfg.Tracing.OTLPProtocol)
	}
	if cfg.Tracing.OTLPHeaders != "x-token=secret" {
		t.Errorf("OTLPHeaders = %q", cfg.Tracing.OTLPHeaders)
	}
	if cfg.Tracing.SampleRatio != 0.5 {
		t.Errorf("SampleRatio = %g", cfg.Tracing.SampleRatio)
	}
	if cfg.Tracing.SlowRequestMS != 250 {
		t.Errorf("SlowRequestMS = %d", cfg.Tracing.SlowRequestMS)
	}
	if cfg.Tracing.RingBufferSize != 50 {
		t.Errorf("RingBufferSize = %d", cfg.Tracing.RingBufferSize)
	}
	if cfg.Tracing.TraceLinkTemplate != "https://tempo.example/{trace_id}" {
		t.Errorf("TraceLinkTemplate = %q", cfg.Tracing.TraceLinkTemplate)
	}
}

func TestScheduler_DefaultTimezoneIsUTC(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Scheduler.DefaultTimezone != "UTC" {
		t.Errorf("Scheduler.DefaultTimezone default = %q, want UTC", cfg.Scheduler.DefaultTimezone)
	}
	if cfg.Scheduler.Location == nil {
		t.Fatal("Scheduler.Location is nil, want non-nil")
	}
	if cfg.Scheduler.Location != time.UTC {
		t.Errorf("Scheduler.Location = %v, want UTC", cfg.Scheduler.Location)
	}
}

func TestScheduler_TimezoneFromYAML(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
scheduler:
  timezone: Europe/Amsterdam
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Scheduler.DefaultTimezone != "Europe/Amsterdam" {
		t.Errorf("Scheduler.DefaultTimezone = %q, want Europe/Amsterdam", cfg.Scheduler.DefaultTimezone)
	}
	if cfg.Scheduler.Location == nil {
		t.Fatal("Scheduler.Location is nil")
	}
	if cfg.Scheduler.Location.String() != "Europe/Amsterdam" {
		t.Errorf("Scheduler.Location = %q, want Europe/Amsterdam", cfg.Scheduler.Location.String())
	}
}

func TestScheduler_TimezoneEnvOverridesYAML(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
scheduler:
  timezone: America/New_York
`)
	t.Setenv("SHINYHUB_SCHEDULER_TIMEZONE", "Asia/Tokyo")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Scheduler.DefaultTimezone != "Asia/Tokyo" {
		t.Errorf("env should override YAML: got %q, want Asia/Tokyo", cfg.Scheduler.DefaultTimezone)
	}
	if cfg.Scheduler.Location.String() != "Asia/Tokyo" {
		t.Errorf("Scheduler.Location = %q, want Asia/Tokyo", cfg.Scheduler.Location.String())
	}
}

func TestScheduler_InvalidTimezoneRejected(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
scheduler:
  timezone: Mars/Olympus
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for invalid scheduler.timezone, got nil")
	}
	if !strings.Contains(err.Error(), "scheduler.timezone") {
		t.Errorf("error should mention scheduler.timezone: %v", err)
	}
}

func TestTracing_EnvOverridesYAML(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
tracing:
  enabled: true
  otlp_endpoint: http://yaml-collector:4318
  sample_ratio: 0.2
`)
	t.Setenv("SHINYHUB_TRACING_SAMPLE_RATIO", "0.8")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tracing.SampleRatio != 0.8 {
		t.Errorf("env should override YAML: got %g, want 0.8", cfg.Tracing.SampleRatio)
	}
	// Untouched YAML fields stay set.
	if cfg.Tracing.OTLPEndpoint != "http://yaml-collector:4318" {
		t.Errorf("YAML endpoint lost: got %q", cfg.Tracing.OTLPEndpoint)
	}
}

func TestRuntimeTiers_Default_WhenAbsent(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tiers := cfg.Runtime.TierOrder()
	if len(tiers) != 1 || tiers[0] != "local" {
		t.Fatalf("expected single synthesized 'local' tier, got %v", tiers)
	}
	if cfg.Runtime.DefaultTierName() != "local" {
		t.Fatalf("default tier = %q, want local", cfg.Runtime.DefaultTierName())
	}
	mode, ok := cfg.Runtime.RuntimeForTier("local")
	if !ok || mode != cfg.Runtime.Mode {
		t.Fatalf("synthesized tier runtime = %q,%v want %q,true", mode, ok, cfg.Runtime.Mode)
	}
}

func TestRuntimeTiers_ParsedInOrder(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	path := writeYAML(t, `
runtime:
  mode: native
  tiers:
    - name: local
      runtime: native
    - name: burst
      runtime: docker
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Runtime.TierOrder(); len(got) != 2 || got[0] != "local" || got[1] != "burst" {
		t.Fatalf("tier order = %v, want [local burst]", got)
	}
	if cfg.Runtime.DefaultTierName() != "local" {
		t.Fatalf("default tier = %q, want local (first declared)", cfg.Runtime.DefaultTierName())
	}
}

func TestConfig_AcceptsRemoteDockerTier(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	path := writeYAML(t, `
runtime:
  mode: native
  tiers:
    - name: local
      runtime: native
    - name: remote
      runtime: remote_docker
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := cfg.Runtime.RuntimeForTier("remote")
	if !ok {
		t.Fatal("RuntimeForTier(remote) = not found")
	}
	if got != "remote_docker" {
		t.Errorf("tier remote mode = %q, want remote_docker", got)
	}
}

func TestRuntimeTiers_RejectsDuplicateAndUnknownRuntime(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate name":  "runtime:\n  tiers:\n    - {name: a, runtime: native}\n    - {name: a, runtime: docker}\n",
		"empty name":      "runtime:\n  tiers:\n    - {name: \"\", runtime: native}\n",
		"unknown runtime": "runtime:\n  tiers:\n    - {name: a, runtime: kubernetes}\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
			path := writeYAML(t, body)
			if _, err := config.Load(path); err == nil {
				t.Fatalf("expected validation error for %q", name)
			}
		})
	}
}

func TestRuntime_Fargate_TierLoadsWithValidConfig(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: cloud
      runtime: fargate
  fargate:
    cluster: shiny-cluster
    task_definition: shiny-app:7
    container_name: app
    subnets: [subnet-a, subnet-b]
    security_groups: [sg-1]
    assign_public_ip: true
    region: eu-west-1
    task_cpu_units: 1024
    task_memory_mb: 2048
    control_plane_url: "https://cp.example.com"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, ok := cfg.Runtime.RuntimeForTier("cloud"); !ok || got != "fargate" {
		t.Fatalf("tier cloud runtime = %q (ok=%v), want fargate", got, ok)
	}
	f := cfg.Runtime.Fargate
	if f.Cluster != "shiny-cluster" || f.TaskDefinition != "shiny-app:7" || f.ContainerName != "app" {
		t.Errorf("fargate core fields = %+v", f)
	}
	if len(f.Subnets) != 2 || f.Subnets[0] != "subnet-a" {
		t.Errorf("fargate subnets = %v", f.Subnets)
	}
	if len(f.SecurityGroups) != 1 || f.SecurityGroups[0] != "sg-1" {
		t.Errorf("fargate security_groups = %v", f.SecurityGroups)
	}
	if !f.AssignPublicIP {
		t.Error("fargate assign_public_ip = false, want true")
	}
	if f.Region != "eu-west-1" {
		t.Errorf("fargate region = %q", f.Region)
	}
}

func TestRuntime_Fargate_TierRejectsMissingFields(t *testing.T) {
	cases := map[string]string{
		"cluster": `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: cloud
      runtime: fargate
  fargate:
    task_definition: shiny-app:7
    container_name: app
    subnets: [subnet-a]
    task_cpu_units: 256
    task_memory_mb: 512
    control_plane_url: "https://cp.example.com"
`,
		"task_definition": `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: cloud
      runtime: fargate
  fargate:
    cluster: shiny-cluster
    container_name: app
    subnets: [subnet-a]
    task_cpu_units: 256
    task_memory_mb: 512
    control_plane_url: "https://cp.example.com"
`,
		"container_name": `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: cloud
      runtime: fargate
  fargate:
    cluster: shiny-cluster
    task_definition: shiny-app:7
    subnets: [subnet-a]
    task_cpu_units: 256
    task_memory_mb: 512
    control_plane_url: "https://cp.example.com"
`,
		"subnets": `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: cloud
      runtime: fargate
  fargate:
    cluster: shiny-cluster
    task_definition: shiny-app:7
    container_name: app
    task_cpu_units: 256
    task_memory_mb: 512
    control_plane_url: "https://cp.example.com"
`,
	}
	for field, yaml := range cases {
		t.Run(field, func(t *testing.T) {
			_, err := config.Load(writeYAML(t, yaml))
			if err == nil {
				t.Fatalf("expected validation error for missing %s", field)
			}
			if !strings.Contains(err.Error(), field) {
				t.Fatalf("error %q should mention %q", err, field)
			}
		})
	}
}

func TestRuntime_Fargate_NoTierDoesNotRequireConfig(t *testing.T) {
	// A fargate config block without any fargate tier must not be validated:
	// native/docker-only deployments leave it empty.
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: local
      runtime: native
`)
	if _, err := config.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestRuntime_Fargate_EnvOverrides(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 32))
	t.Setenv("SHINYHUB_RUNTIME_FARGATE_CLUSTER", "env-cluster")
	t.Setenv("SHINYHUB_RUNTIME_FARGATE_SUBNETS", "subnet-x, subnet-y")
	t.Setenv("SHINYHUB_RUNTIME_FARGATE_ASSIGN_PUBLIC_IP", "true")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Fargate.Cluster != "env-cluster" {
		t.Errorf("cluster from env = %q", cfg.Runtime.Fargate.Cluster)
	}
	if len(cfg.Runtime.Fargate.Subnets) != 2 || cfg.Runtime.Fargate.Subnets[1] != "subnet-y" {
		t.Errorf("subnets from env = %v (want trimmed [subnet-x subnet-y])", cfg.Runtime.Fargate.Subnets)
	}
	if !cfg.Runtime.Fargate.AssignPublicIP {
		t.Error("assign_public_ip from env = false, want true")
	}
}

func TestRuntime_Tier_RejectsUnknownRuntimeMentionsFargate(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: cloud
      runtime: kubernetes
`)
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for unknown tier runtime")
	}
	if !strings.Contains(err.Error(), "fargate") {
		t.Fatalf("error should list fargate as an option, got %v", err)
	}
}

func TestApplyEnvNumericErrorsAreFatal(t *testing.T) {
	cases := []struct {
		name   string
		envKey string
		badVal string
	}{
		{"docker default memory", "SHINYHUB_RUNTIME_DOCKER_DEFAULT_MEMORY_MB", "512m"},
		{"docker default cpu", "SHINYHUB_RUNTIME_DOCKER_DEFAULT_CPU_PERCENT", "50pct"},
		{"version retention", "SHINYHUB_STORAGE_VERSION_RETENTION", "five"},
		{"app quota", "SHINYHUB_APP_QUOTA_MB", "1G"},
		{"max bundle mb", "SHINYHUB_MAX_BUNDLE_MB", "128mb"},
		{"default replicas", "SHINYHUB_RUNTIME_DEFAULT_REPLICAS", "two"},
		{"max replicas", "SHINYHUB_RUNTIME_MAX_REPLICAS", "100x"},
		{"default max sessions", "SHINYHUB_RUNTIME_DEFAULT_MAX_SESSIONS_PER_REPLICA", "ten"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
			t.Setenv(tc.envKey, tc.badVal)
			if _, err := config.Load(""); err == nil {
				t.Errorf("Load with %s=%q: expected error for non-integer value, got nil", tc.envKey, tc.badVal)
			}
		})
	}

	// SHINYHUB_RUNTIME_DEFAULT_REPLICAS intentionally ignores n<=0 values (the
	// n>0 guard keeps the compiled default of 1). A valid integer that fails the
	// guard must not be an error; Load must succeed and the field must keep its
	// compiled default.
	t.Run("default replicas zero uses compiled default", func(t *testing.T) {
		t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
		t.Setenv("SHINYHUB_RUNTIME_DEFAULT_REPLICAS", "0")
		cfg, err := config.Load("")
		if err != nil {
			t.Fatalf("Load with SHINYHUB_RUNTIME_DEFAULT_REPLICAS=0: expected success, got %v", err)
		}
		// The compiled default is 1; zero is parsed but silently ignored by the n>0 guard.
		if cfg.Runtime.DefaultReplicas != 1 {
			t.Errorf("DefaultReplicas = %d, want 1 (compiled default when env value fails the n>0 guard)", cfg.Runtime.DefaultReplicas)
		}
	})
}

func TestFargateConfig_NewFields_YAMLRoundTrip(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: burst
      runtime: fargate
  fargate:
    cluster: my-cluster
    task_definition: my-td
    container_name: app
    subnets: [subnet-1]
    task_cpu_units: 1024
    task_memory_mb: 2048
    default_memory_mb: 512
    default_cpu_percent: 50
    control_plane_url: "https://cp.example.com"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Fargate.TaskCPUUnits != 1024 {
		t.Errorf("TaskCPUUnits: got %d, want 1024", cfg.Runtime.Fargate.TaskCPUUnits)
	}
	if cfg.Runtime.Fargate.TaskMemoryMB != 2048 {
		t.Errorf("TaskMemoryMB: got %d, want 2048", cfg.Runtime.Fargate.TaskMemoryMB)
	}
	if cfg.Runtime.Fargate.DefaultMemoryMB != 512 {
		t.Errorf("DefaultMemoryMB: got %d, want 512", cfg.Runtime.Fargate.DefaultMemoryMB)
	}
	if cfg.Runtime.Fargate.DefaultCPUPercent != 50 {
		t.Errorf("DefaultCPUPercent: got %d, want 50", cfg.Runtime.Fargate.DefaultCPUPercent)
	}
}

func TestFargateConfig_Secrets_YAMLRoundTrip(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: burst
      runtime: fargate
  fargate:
    cluster: my-cluster
    task_definition: my-td
    container_name: app
    subnets: [subnet-1]
    task_cpu_units: 1024
    task_memory_mb: 2048
    control_plane_url: "https://cp.example.com"
    secrets:
      name_prefix: shinyhub/prod
      kms_key_id: alias/shinyhub
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Fargate.SecretsNamePrefix != "shinyhub/prod" {
		t.Errorf("SecretsNamePrefix: got %q, want shinyhub/prod", cfg.Runtime.Fargate.SecretsNamePrefix)
	}
	if cfg.Runtime.Fargate.SecretsKMSKeyID != "alias/shinyhub" {
		t.Errorf("SecretsKMSKeyID: got %q, want alias/shinyhub", cfg.Runtime.Fargate.SecretsKMSKeyID)
	}
}

func TestFargateConfig_Secrets_EnvOverride(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_RUNTIME_FARGATE_SECRETS_NAME_PREFIX", "shinyhub/staging")
	t.Setenv("SHINYHUB_RUNTIME_FARGATE_SECRETS_KMS_KEY_ID", "arn:aws:kms:eu-west-1:111122223333:key/abc")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Fargate.SecretsNamePrefix != "shinyhub/staging" {
		t.Errorf("SecretsNamePrefix from env: got %q", cfg.Runtime.Fargate.SecretsNamePrefix)
	}
	if cfg.Runtime.Fargate.SecretsKMSKeyID != "arn:aws:kms:eu-west-1:111122223333:key/abc" {
		t.Errorf("SecretsKMSKeyID from env: got %q", cfg.Runtime.Fargate.SecretsKMSKeyID)
	}
}

func TestFargateConfig_NewFields_EnvOverride(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_RUNTIME_FARGATE_TASK_CPU_UNITS", "2048")
	t.Setenv("SHINYHUB_RUNTIME_FARGATE_TASK_MEMORY_MB", "4096")
	t.Setenv("SHINYHUB_RUNTIME_FARGATE_DEFAULT_MEMORY_MB", "1024")
	t.Setenv("SHINYHUB_RUNTIME_FARGATE_DEFAULT_CPU_PERCENT", "75")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Fargate.TaskCPUUnits != 2048 {
		t.Errorf("TaskCPUUnits from env: got %d, want 2048", cfg.Runtime.Fargate.TaskCPUUnits)
	}
	if cfg.Runtime.Fargate.TaskMemoryMB != 4096 {
		t.Errorf("TaskMemoryMB from env: got %d, want 4096", cfg.Runtime.Fargate.TaskMemoryMB)
	}
	if cfg.Runtime.Fargate.DefaultMemoryMB != 1024 {
		t.Errorf("DefaultMemoryMB from env: got %d, want 1024", cfg.Runtime.Fargate.DefaultMemoryMB)
	}
	if cfg.Runtime.Fargate.DefaultCPUPercent != 75 {
		t.Errorf("DefaultCPUPercent from env: got %d, want 75", cfg.Runtime.Fargate.DefaultCPUPercent)
	}
}

func TestFargateConfig_EnvBadInteger_ReturnsError(t *testing.T) {
	cases := []struct {
		env string
		val string
	}{
		{"SHINYHUB_RUNTIME_FARGATE_TASK_CPU_UNITS", "not-a-number"},
		{"SHINYHUB_RUNTIME_FARGATE_TASK_MEMORY_MB", "12.5"},
		{"SHINYHUB_RUNTIME_FARGATE_DEFAULT_MEMORY_MB", "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
			t.Setenv(tc.env, tc.val)
			_, err := config.Load("")
			if err == nil {
				t.Errorf("Load with %s=%q: want error, got nil", tc.env, tc.val)
			}
			if !strings.Contains(err.Error(), tc.env) {
				t.Errorf("error %q does not mention env var %s", err.Error(), tc.env)
			}
		})
	}
}

// minimalFargateYAML returns a YAML string with a valid fargate tier and the
// given cpu/mem values so matrix tests don't repeat the boilerplate.
func minimalFargateYAML(cpuUnits, memMB int) string {
	return fmt.Sprintf(`
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: burst
      runtime: fargate
  fargate:
    cluster: c
    task_definition: td
    container_name: app
    subnets: [s-1]
    control_plane_url: "https://cp.example.com"
    task_cpu_units: %d
    task_memory_mb: %d
`, cpuUnits, memMB)
}

func TestValidateFargate_Matrix(t *testing.T) {
	valid := []struct {
		cpu int
		mem int
	}{
		{256, 512},
		{256, 1024},
		{256, 2048},
		{512, 1024},
		{512, 4096},
		{1024, 2048},
		{1024, 8192},
		{2048, 4096},
		{2048, 16384},
		{4096, 8192},
		{4096, 30720},
		{8192, 16384},
		{8192, 61440},
		{16384, 32768},
		{16384, 122880},
	}
	for _, tc := range valid {
		t.Run(fmt.Sprintf("valid_%d_%d", tc.cpu, tc.mem), func(t *testing.T) {
			path := writeYAML(t, minimalFargateYAML(tc.cpu, tc.mem))
			if _, err := config.Load(path); err != nil {
				t.Errorf("expected valid matrix entry cpu=%d mem=%d to load without error, got: %v", tc.cpu, tc.mem, err)
			}
		})
	}

	invalid := []struct {
		cpu int
		mem int
		msg string
	}{
		// cpu not in allowed set
		{300, 512, "unsupported cpu"},
		// mem below minimum
		{512, 512, "below minimum"},
		// mem above maximum
		{512, 8192, "above maximum"},
		// increment violation: 8192 cpu must be multiple of 4096 above base 16384
		// 17000 is 16384 + 616; 616 % 4096 != 0
		{8192, 17000, "increment violation"},
		// increment violation: 16384 cpu must be multiple of 8192 above base 32768
		// 33000 is 32768 + 232; 232 % 8192 != 0
		{16384, 33000, "increment violation"},
		// 256 cpu discrete set: 1536 is not in {512,1024,2048}
		{256, 1536, "not in discrete set"},
		// 2048 cpu: 5000 is 4096+904; 904 % 1024 != 0
		{2048, 5000, "increment violation"},
		// 512 cpu (1024-step tier): 1536 is 1024+512; 512 % 1024 != 0
		{512, 1536, "increment violation"},
		// 1024 cpu (1024-step tier): 2560 is 2048+512; 512 % 1024 != 0
		{1024, 2560, "increment violation"},
	}
	for _, tc := range invalid {
		t.Run(fmt.Sprintf("invalid_%d_%d_%s", tc.cpu, tc.mem, tc.msg), func(t *testing.T) {
			path := writeYAML(t, minimalFargateYAML(tc.cpu, tc.mem))
			_, err := config.Load(path)
			if err == nil {
				t.Errorf("expected invalid matrix entry cpu=%d mem=%d (%s) to fail, but Load succeeded", tc.cpu, tc.mem, tc.msg)
			}
		})
	}
}

func TestValidateFargate_RequiresTaskCPUAndMemory(t *testing.T) {
	// task_cpu_units present but task_memory_mb absent
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: burst
      runtime: fargate
  fargate:
    cluster: c
    task_definition: td
    container_name: app
    subnets: [s-1]
    control_plane_url: "https://cp.example.com"
    task_cpu_units: 1024
`)
	if _, err := config.Load(path); err == nil {
		t.Error("expected error when task_memory_mb is 0 (absent), got nil")
	}

	// task_memory_mb present but task_cpu_units absent
	path2 := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: burst
      runtime: fargate
  fargate:
    cluster: c
    task_definition: td
    container_name: app
    subnets: [s-1]
    control_plane_url: "https://cp.example.com"
    task_memory_mb: 2048
`)
	if _, err := config.Load(path2); err == nil {
		t.Error("expected error when task_cpu_units is 0 (absent), got nil")
	}
}

func TestDefaultResourcesForTier(t *testing.T) {
	t.Run("native_tier_returns_docker_defaults", func(t *testing.T) {
		t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
		cfg, err := config.Load("")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		// synthesized single "local" tier with runtime=native
		mem, cpu := cfg.Runtime.DefaultResourcesForTier("local")
		if mem != cfg.Runtime.Docker.DefaultMemoryMB {
			t.Errorf("mem: got %d, want %d", mem, cfg.Runtime.Docker.DefaultMemoryMB)
		}
		if cpu != cfg.Runtime.Docker.DefaultCPUPercent {
			t.Errorf("cpu: got %d, want %d", cpu, cfg.Runtime.Docker.DefaultCPUPercent)
		}
	})

	t.Run("docker_tier_returns_docker_defaults", func(t *testing.T) {
		path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: workers
      runtime: docker
  docker:
    default_memory_mb: 256
    default_cpu_percent: 25
`)
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		mem, cpu := cfg.Runtime.DefaultResourcesForTier("workers")
		if mem != 256 {
			t.Errorf("mem: got %d, want 256", mem)
		}
		if cpu != 25 {
			t.Errorf("cpu: got %d, want 25", cpu)
		}
	})

	t.Run("fargate_tier_returns_fargate_defaults", func(t *testing.T) {
		path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: burst
      runtime: fargate
  fargate:
    cluster: c
    task_definition: td
    container_name: app
    subnets: [s-1]
    task_cpu_units: 1024
    task_memory_mb: 2048
    default_memory_mb: 512
    default_cpu_percent: 50
    control_plane_url: "https://cp.example.com"
`)
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		mem, cpu := cfg.Runtime.DefaultResourcesForTier("burst")
		if mem != 512 {
			t.Errorf("mem: got %d, want 512", mem)
		}
		if cpu != 50 {
			t.Errorf("cpu: got %d, want 50", cpu)
		}
	})

	t.Run("unknown_tier_returns_docker_defaults", func(t *testing.T) {
		t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
		cfg, err := config.Load("")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		mem, cpu := cfg.Runtime.DefaultResourcesForTier("nonexistent")
		if mem != cfg.Runtime.Docker.DefaultMemoryMB {
			t.Errorf("mem: got %d, want Docker default %d", mem, cfg.Runtime.Docker.DefaultMemoryMB)
		}
		if cpu != cfg.Runtime.Docker.DefaultCPUPercent {
			t.Errorf("cpu: got %d, want Docker default %d", cpu, cfg.Runtime.Docker.DefaultCPUPercent)
		}
	})
}

// loadFromString writes yaml to a temp file and calls config.Load.
func loadFromString(t *testing.T, yaml string) (*config.Config, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(yaml)
	f.Close()
	return config.Load(f.Name())
}

func TestFargateConfig_ControlPlaneURLRequired(t *testing.T) {
	// A fargate tier with no control_plane_url must fail Load.
	yaml := `
auth:
  secret: "aaaabbbbccccddddeeeeffffgggghhhh"
runtime:
  tiers:
    - name: cloud
      runtime: fargate
  fargate:
    cluster: my-cluster
    task_definition: my-task:1
    container_name: app
    subnets: [subnet-abc]
    task_cpu_units: 256
    task_memory_mb: 512
`
	_, err := loadFromString(t, yaml)
	if err == nil {
		t.Fatal("expected error for missing control_plane_url, got nil")
	}
	if !strings.Contains(err.Error(), "control_plane_url") {
		t.Fatalf("error message must mention control_plane_url, got: %v", err)
	}
}

func TestFargateConfig_RouteViaPublicIPRequiresHTTPS(t *testing.T) {
	yaml := `
auth:
  secret: "aaaabbbbccccddddeeeeffffgggghhhh"
runtime:
  tiers:
    - name: cloud
      runtime: fargate
  fargate:
    cluster: my-cluster
    task_definition: my-task:1
    container_name: app
    subnets: [subnet-abc]
    assign_public_ip: true
    route_via_public_ip: true
    control_plane_url: "http://1.2.3.4:8080"
    task_cpu_units: 256
    task_memory_mb: 512
`
	_, err := loadFromString(t, yaml)
	if err == nil {
		t.Fatal("expected error for http:// with route_via_public_ip, got nil")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Fatalf("error message must mention https, got: %v", err)
	}
}

func TestFargateConfig_RouteViaPublicIPAcceptsHTTPS(t *testing.T) {
	yaml := `
auth:
  secret: "aaaabbbbccccddddeeeeffffgggghhhh"
runtime:
  tiers:
    - name: cloud
      runtime: fargate
  fargate:
    cluster: my-cluster
    task_definition: my-task:1
    container_name: app
    subnets: [subnet-abc]
    assign_public_ip: true
    route_via_public_ip: true
    control_plane_url: "https://example.com"
    task_cpu_units: 256
    task_memory_mb: 512
`
	cfg, err := loadFromString(t, yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Runtime.Fargate.ControlPlaneURL != "https://example.com" {
		t.Fatalf("ControlPlaneURL not parsed, got %q", cfg.Runtime.Fargate.ControlPlaneURL)
	}
}

func TestFargateConfig_BundleTokenTTLDefault(t *testing.T) {
	yaml := `
auth:
  secret: "aaaabbbbccccddddeeeeffffgggghhhh"
runtime:
  tiers:
    - name: cloud
      runtime: fargate
  fargate:
    cluster: my-cluster
    task_definition: my-task:1
    container_name: app
    subnets: [subnet-abc]
    control_plane_url: "https://example.com"
    task_cpu_units: 256
    task_memory_mb: 512
`
	cfg, err := loadFromString(t, yaml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Runtime.Fargate.BundleTokenTTL != 10*time.Minute {
		t.Fatalf("want default BundleTokenTTL=10m, got %v", cfg.Runtime.Fargate.BundleTokenTTL)
	}
}

func TestFargateConfig_BundleTokenTTLEnvBadValue(t *testing.T) {
	t.Setenv("SHINYHUB_RUNTIME_FARGATE_BUNDLE_TOKEN_TTL", "notaduration")
	yaml := `
auth:
  secret: "aaaabbbbccccddddeeeeffffgggghhhh"
runtime:
  tiers:
    - name: cloud
      runtime: fargate
  fargate:
    cluster: my-cluster
    task_definition: my-task:1
    container_name: app
    subnets: [subnet-abc]
    control_plane_url: "https://example.com"
    task_cpu_units: 256
    task_memory_mb: 512
`
	_, err := loadFromString(t, yaml)
	if err == nil {
		t.Fatal("expected error for bad duration env var, got nil")
	}
	if !strings.Contains(err.Error(), "SHINYHUB_RUNTIME_FARGATE_BUNDLE_TOKEN_TTL") {
		t.Fatalf("error must name the env var, got: %v", err)
	}
}

func TestFargateConfig_BundleTokenTTLEnvNegative(t *testing.T) {
	t.Setenv("SHINYHUB_RUNTIME_FARGATE_BUNDLE_TOKEN_TTL", "-1m")
	yaml := `
auth:
  secret: "aaaabbbbccccddddeeeeffffgggghhhh"
runtime:
  tiers:
    - name: cloud
      runtime: fargate
  fargate:
    cluster: my-cluster
    task_definition: my-task:1
    container_name: app
    subnets: [subnet-abc]
    control_plane_url: "https://example.com"
    task_cpu_units: 256
    task_memory_mb: 512
`
	_, err := loadFromString(t, yaml)
	if err == nil {
		t.Fatal("expected error for negative bundle_token_ttl, got nil")
	}
	if !strings.Contains(err.Error(), "SHINYHUB_RUNTIME_FARGATE_BUNDLE_TOKEN_TTL") {
		t.Fatalf("error must name the env var, got: %v", err)
	}
}

// placementJSON serialises a {tier: count} map for use in db.App.ReplicaPlacement.
func placementJSON(t *testing.T, m map[string]int) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal placement: %v", err)
	}
	return string(b)
}

// TestDefaultResourcesForApp_PlacementAwareResolution verifies that
// DefaultResourcesForApp resolves defaults from the app's actual placement
// tier rather than always using the global default tier.
//
// Config: two tiers - "local" (native, default) and "burst" (fargate).
// Fargate defaults: 512 MiB, 50%.
// Docker/native defaults: 0 (no limit).
// These are intentionally distinct so the test can tell them apart.
func TestDefaultResourcesForApp_PlacementAwareResolution(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: local
      runtime: native
    - name: burst
      runtime: fargate
  docker:
    default_memory_mb: 0
    default_cpu_percent: 0
  fargate:
    cluster: shiny-cluster
    task_definition: shiny-app:1
    container_name: app
    subnets: [subnet-a]
    control_plane_url: "https://cp.192.0.2.1.example.com"
    task_cpu_units: 256
    task_memory_mb: 512
    default_memory_mb: 512
    default_cpu_percent: 50
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	t.Run("single fargate tier placement resolves fargate defaults", func(t *testing.T) {
		app := &db.App{
			ReplicaPlacement: placementJSON(t, map[string]int{"burst": 2}),
		}
		mem, cpu := cfg.Runtime.DefaultResourcesForApp(app)
		if mem != 512 {
			t.Errorf("mem: got %d, want 512 (fargate default)", mem)
		}
		if cpu != 50 {
			t.Errorf("cpu: got %d, want 50 (fargate default)", cpu)
		}
	})

	t.Run("no placement falls back to default tier (native, zeros)", func(t *testing.T) {
		app := &db.App{
			ReplicaPlacement: "",
		}
		mem, cpu := cfg.Runtime.DefaultResourcesForApp(app)
		if mem != 0 {
			t.Errorf("mem: got %d, want 0 (native/docker default)", mem)
		}
		if cpu != 0 {
			t.Errorf("cpu: got %d, want 0 (native/docker default)", cpu)
		}
	})

	t.Run("multi-tier placement falls back to default tier (native, zeros)", func(t *testing.T) {
		app := &db.App{
			ReplicaPlacement: placementJSON(t, map[string]int{"local": 1, "burst": 1}),
		}
		mem, cpu := cfg.Runtime.DefaultResourcesForApp(app)
		if mem != 0 {
			t.Errorf("mem: got %d, want 0 (native/docker default for multi-tier fallback)", mem)
		}
		if cpu != 0 {
			t.Errorf("cpu: got %d, want 0 (native/docker default for multi-tier fallback)", cpu)
		}
	})

	t.Run("single native tier placement resolves native defaults", func(t *testing.T) {
		app := &db.App{
			ReplicaPlacement: placementJSON(t, map[string]int{"local": 3}),
		}
		mem, cpu := cfg.Runtime.DefaultResourcesForApp(app)
		if mem != 0 {
			t.Errorf("mem: got %d, want 0 (native default)", mem)
		}
		if cpu != 0 {
			t.Errorf("cpu: got %d, want 0 (native default)", cpu)
		}
	})

	t.Run("malformed placement JSON falls back to default tier", func(t *testing.T) {
		app := &db.App{
			ReplicaPlacement: `{"bad json`,
		}
		mem, cpu := cfg.Runtime.DefaultResourcesForApp(app)
		if mem != 0 {
			t.Errorf("mem: got %d, want 0 (default tier fallback on malformed JSON)", mem)
		}
		if cpu != 0 {
			t.Errorf("cpu: got %d, want 0 (default tier fallback on malformed JSON)", cpu)
		}
	})

	t.Run("nil app falls back to default tier (native, zeros)", func(t *testing.T) {
		mem, cpu := cfg.Runtime.DefaultResourcesForApp(nil)
		if mem != 0 {
			t.Errorf("mem: got %d, want 0 (native default tier on nil app)", mem)
		}
		if cpu != 0 {
			t.Errorf("cpu: got %d, want 0 (native default tier on nil app)", cpu)
		}
	})
}

// TestDefaultResourcesForApp_SingleTierFargate verifies the common single-tier
// fargate deployment - where the global default IS the fargate tier - so that
// results from DefaultResourcesForApp and DefaultResourcesForTier are identical.
func TestDefaultResourcesForApp_SingleTierFargate(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: cloud
      runtime: fargate
  fargate:
    cluster: shiny-cluster
    task_definition: shiny-app:1
    container_name: app
    subnets: [subnet-a]
    control_plane_url: "https://cp.192.0.2.1.example.com"
    task_cpu_units: 256
    task_memory_mb: 512
    default_memory_mb: 256
    default_cpu_percent: 25
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	app := &db.App{
		ReplicaPlacement: placementJSON(t, map[string]int{"cloud": 1}),
	}
	mem, cpu := cfg.Runtime.DefaultResourcesForApp(app)
	if mem != 256 {
		t.Errorf("mem: got %d, want 256", mem)
	}
	if cpu != 25 {
		t.Errorf("cpu: got %d, want 25", cpu)
	}
}

// TestTierConfig_LaunchTypeEC2 asserts that a tier with launch_type: EC2 parses
// correctly and the TierConfig carries the EC2 value.
func TestTierConfig_LaunchTypeEC2(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: burst
      runtime: fargate
      launch_type: EC2
  fargate:
    cluster: my-cluster
    task_definition: my-td
    container_name: app
    subnets: [subnet-1]
    control_plane_url: https://example.com
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Runtime.Tiers) != 1 {
		t.Fatalf("got %d tiers, want 1", len(cfg.Runtime.Tiers))
	}
	if cfg.Runtime.Tiers[0].LaunchType != "EC2" {
		t.Errorf("LaunchType = %q, want EC2", cfg.Runtime.Tiers[0].LaunchType)
	}
}

// TestValidateFargate_EC2TierDoesNotRequireMatrix asserts that an EC2 tier
// validates without task_cpu_units/task_memory_mb (they are optional for EC2).
func TestValidateFargate_EC2TierDoesNotRequireMatrix(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: gpu
      runtime: fargate
      launch_type: EC2
  fargate:
    cluster: my-cluster
    task_definition: my-td
    container_name: app
    subnets: [subnet-1]
    control_plane_url: https://example.com
`)
	if _, err := config.Load(path); err != nil {
		t.Fatalf("EC2 tier without task_cpu_units/task_memory_mb must be valid; got: %v", err)
	}
}

// TestValidateFargate_FargateTierStillEnforcesMatrix asserts (regression) that
// a FARGATE tier still requires task_cpu_units and task_memory_mb and they must
// satisfy the Fargate CPU/memory matrix.
func TestValidateFargate_FargateTierStillEnforcesMatrix(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: burst
      runtime: fargate
      launch_type: FARGATE
  fargate:
    cluster: my-cluster
    task_definition: my-td
    container_name: app
    subnets: [subnet-1]
    control_plane_url: https://example.com
    task_cpu_units: 256
    task_memory_mb: 99999
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("FARGATE tier with invalid memory must fail validation")
	}
}

// TestValidateFargate_PlatformVersionRejectedWhenAllEC2 asserts that
// platform_version is rejected when all fargate tiers are EC2 launch type.
func TestValidateFargate_PlatformVersionRejectedWhenAllEC2(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: gpu
      runtime: fargate
      launch_type: EC2
  fargate:
    cluster: my-cluster
    task_definition: my-td
    container_name: app
    subnets: [subnet-1]
    control_plane_url: https://example.com
    platform_version: "1.4.0"
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("platform_version must be rejected when all tiers are EC2")
	}
}

// TestValidateFargate_MixedFargateAndEC2Valid asserts that a config with one
// FARGATE tier and one EC2 tier on the same shared fargate block validates:
// matrix applies only to FARGATE tiers; EC2 tiers are exempt.
func TestValidateFargate_MixedFargateAndEC2Valid(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: burst
      runtime: fargate
      launch_type: FARGATE
    - name: gpu
      runtime: fargate
      launch_type: EC2
  fargate:
    cluster: my-cluster
    task_definition: my-td
    container_name: app
    subnets: [subnet-1]
    control_plane_url: https://example.com
    task_cpu_units: 1024
    task_memory_mb: 2048
`)
	if _, err := config.Load(path); err != nil {
		t.Fatalf("mixed FARGATE+EC2 config must be valid: %v", err)
	}
}

// TestApplyEnv_FargateLaunchTypeDefault asserts that
// SHINYHUB_RUNTIME_FARGATE_LAUNCH_TYPE sets the default launch type for tiers
// that do not specify launch_type explicitly.
func TestApplyEnv_FargateLaunchTypeDefault(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_RUNTIME_FARGATE_LAUNCH_TYPE", "EC2")
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: gpu
      runtime: fargate
  fargate:
    cluster: my-cluster
    task_definition: my-td
    container_name: app
    subnets: [subnet-1]
    control_plane_url: https://example.com
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Runtime.Tiers[0].LaunchType != "EC2" {
		t.Errorf("LaunchType = %q, want EC2 (from env default)", cfg.Runtime.Tiers[0].LaunchType)
	}
}

// TestTierConfig_BadLaunchTypeReturnsError asserts that an unrecognised
// launch_type value returns a config error.
func TestTierConfig_BadLaunchTypeReturnsError(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  tiers:
    - name: burst
      runtime: fargate
      launch_type: INVALID
  fargate:
    cluster: my-cluster
    task_definition: my-td
    container_name: app
    subnets: [subnet-1]
    control_plane_url: https://example.com
    task_cpu_units: 1024
    task_memory_mb: 2048
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("bad launch_type must return a config error")
	}
}

// TestValidateFargate_NoFargateTiersDoesNotRejectPlatformVersion guards the
// allEC2 logic: a tiers slice that contains no fargate tiers must not
// spuriously reject platform_version (allEC2 must be false when
// fargateTierCount == 0). This prevents a regression where an empty or
// fargate-free tier list incorrectly set allEC2 = true and caused valid
// configs to fail validation.
//
// Tested via Load with no fargate tier declared, so validateFargate is never
// called and the fargate block with platform_version is simply unused. This
// documents the contract: platform_version is only rejected when ALL fargate
// tiers are EC2.
func TestValidateFargate_NoFargateTiersDoesNotRejectPlatformVersion(t *testing.T) {
	path := writeYAML(t, `
auth:
  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
runtime:
  mode: native
  fargate:
    cluster: my-cluster
    task_definition: my-td
    container_name: app
    subnets: [subnet-1]
    control_plane_url: https://example.com
    platform_version: "1.4.0"
`)
	if _, err := config.Load(path); err != nil {
		t.Fatalf("platform_version on an unused fargate block must not fail validation: %v", err)
	}
}

func TestForwardAuth_DefaultsWhenEnabled(t *testing.T) {
	yaml := `
auth:
  secret: ` + strings.Repeat("a", 32) + `
  forward_auth:
    enabled: true
`
	f := writeYAML(t, yaml)
	cfg, err := config.Load(f)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Auth.ForwardAuth.Enabled {
		t.Fatal("expected enabled")
	}
	if got, want := cfg.Auth.ForwardAuth.UserHeader, "X-Forwarded-User"; got != want {
		t.Fatalf("UserHeader: got %q want %q", got, want)
	}
	if got, want := cfg.Auth.ForwardAuth.DefaultRole, "developer"; got != want {
		t.Fatalf("DefaultRole: got %q want %q", got, want)
	}
}

func TestForwardAuth_RejectsInvalidDefaultRole(t *testing.T) {
	yaml := `
auth:
  secret: ` + strings.Repeat("a", 32) + `
  forward_auth:
    enabled: true
    default_role: banana
`
	f := writeYAML(t, yaml)
	if _, err := config.Load(f); err == nil {
		t.Fatal("expected error for invalid default_role")
	}
}

func TestForwardAuth_EnvOverride(t *testing.T) {
	t.Setenv("SHINYHUB_FORWARD_AUTH_ENABLED", "true")
	t.Setenv("SHINYHUB_FORWARD_AUTH_USER_HEADER", "X-User")
	t.Setenv("SHINYHUB_FORWARD_AUTH_ADMIN_GROUPS", "admins,sre")
	yaml := `
auth:
  secret: ` + strings.Repeat("a", 32) + `
`
	f := writeYAML(t, yaml)
	cfg, err := config.Load(f)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Auth.ForwardAuth.Enabled {
		t.Fatal("expected env to enable")
	}
	if got, want := cfg.Auth.ForwardAuth.UserHeader, "X-User"; got != want {
		t.Fatalf("UserHeader: got %q want %q", got, want)
	}
	if got, want := strings.Join(cfg.Auth.ForwardAuth.AdminGroups, ","), "admins,sre"; got != want {
		t.Fatalf("AdminGroups: got %q want %q", got, want)
	}
}

func TestLoad_NormalizesStorageRootsToAbsolute(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("auth:\n  secret: "+strings.Repeat("a", 32)+"\nstorage:\n  apps_dir: ./rel-apps\n  app_data_dir: ./rel-data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(cfg.Storage.AppsDir) {
		t.Errorf("apps_dir not absolute: %q", cfg.Storage.AppsDir)
	}
	if !filepath.IsAbs(cfg.Storage.AppDataDir) {
		t.Errorf("app_data_dir not absolute: %q", cfg.Storage.AppDataDir)
	}
}

func TestGroupRoleMappings_FromEnv(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_AUTH_GROUP_ROLE_MAPPINGS", "shinyhub-admins:admin,data-sci:developer")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.Auth.GroupRoleMappings
	if len(got) != 2 || got[0] != (config.GroupRoleMapping{Group: "shinyhub-admins", Role: "admin"}) ||
		got[1] != (config.GroupRoleMapping{Group: "data-sci", Role: "developer"}) {
		t.Fatalf("mappings = %+v", got)
	}
}

func TestGroupRoleMappings_RejectsInvalidRole(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_AUTH_GROUP_ROLE_MAPPINGS", "g:superuser")
	if _, err := config.Load(""); err == nil {
		t.Fatal("expected error for invalid mapped role")
	}
}

func TestAdminGroupsAliasMergesIntoMappings(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_FORWARD_AUTH_ENABLED", "true")
	t.Setenv("SHINYHUB_FORWARD_AUTH_ADMIN_GROUPS", "legacy-admins")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, m := range cfg.Auth.GroupRoleMappings {
		if m.Group == "legacy-admins" && m.Role == "admin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("admin_groups alias not merged: %+v", cfg.Auth.GroupRoleMappings)
	}
}

func TestOIDCGroupsClaimDefault(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_OIDC_ISSUER_URL", "https://idp.example.com")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OAuth.OIDC.GroupsClaim != "groups" {
		t.Fatalf("GroupsClaim = %q, want default \"groups\"", cfg.OAuth.OIDC.GroupsClaim)
	}
}
