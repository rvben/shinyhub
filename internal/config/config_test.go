package config_test

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
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
	if got, want := cfg.Storage.AppDataDir, "./data/app-data"; got != want {
		t.Errorf("AppDataDir default = %q, want %q", got, want)
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
