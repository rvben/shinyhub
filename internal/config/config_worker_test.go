// internal/config/config_worker_test.go
package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWorkerConfigParsed(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "shinyhub.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
auth:
  secret: "0123456789abcdef0123456789abcdef"
worker:
  enabled: true
  join_token_file: /etc/shinyhub/join-token
  ca_dir: /var/lib/shinyhub/ca
  advertise_hosts:
    - cp.example.com
    - 10.0.0.1
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Worker.Enabled || cfg.Worker.JoinTokenFile != "/etc/shinyhub/join-token" || cfg.Worker.CADir != "/var/lib/shinyhub/ca" {
		t.Fatalf("worker config = %+v", cfg.Worker)
	}
	want := []string{"cp.example.com", "10.0.0.1"}
	if !reflect.DeepEqual(cfg.Worker.AdvertiseHosts, want) {
		t.Fatalf("advertise_hosts = %v, want %v", cfg.Worker.AdvertiseHosts, want)
	}
}

func TestWorkerAdvertiseHostsEnvOverride(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("SHINYHUB_WORKER_ADVERTISE_HOSTS", "a, b , ")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []string{"a", "b"}
	if !reflect.DeepEqual(cfg.Worker.AdvertiseHosts, want) {
		t.Fatalf("advertise_hosts from env = %v, want %v", cfg.Worker.AdvertiseHosts, want)
	}
}
