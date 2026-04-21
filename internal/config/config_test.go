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
