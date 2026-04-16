package config_test

import (
	"net"
	"os"
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
	t.Setenv("SHINYHUB_AUTH_SECRET", "test-secret")
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
  secret: test-secret
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
  secret: test-secret
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
  secret: test-secret
lifecycle:
  watch_interval: "not-a-duration"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Error("expected error for invalid duration, got nil")
	}
}

func TestTrustedProxies_Default(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "test-secret")
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
  secret: test-secret
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
  secret: test-secret
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
	t.Setenv("SHINYHUB_AUTH_SECRET", "test-secret")
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
	t.Setenv("SHINYHUB_AUTH_SECRET", "test-secret")
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
	t.Setenv("SHINYHUB_AUTH_SECRET", "test-secret")

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
