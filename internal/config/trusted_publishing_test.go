package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

func TestTrustedPublisherConfigRejectsUnscopedPolicy(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "12345678901234567890123456789012")
	path := filepath.Join(t.TempDir(), "config.yaml")
	base := `auth:
  trusted_publishers:
    - name: production
      issuer: https://ci.example
      jwks_url: https://ci.example/keys
      audience: https://hub.example
      subject: project:123:main
`
	if err := os.WriteFile(path, []byte(base), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("unscoped publisher accepted")
	}
	if err := os.WriteFile(path, []byte(base+"      apps: [sales]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Auth.TrustedPublishers) != 1 {
		t.Fatal("publisher not loaded")
	}
}
