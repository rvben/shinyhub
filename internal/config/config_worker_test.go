// internal/config/config_worker_test.go
package config

import (
	"os"
	"path/filepath"
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
}
