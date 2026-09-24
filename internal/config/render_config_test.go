package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderConfigDefaults(t *testing.T) {
	c := &Config{} // zero server config: every accessor must return its default
	if h := c.RenderHeadroom(); h != 0.75 {
		t.Fatalf("RenderHeadroom default = %v, want 0.75", h)
	}
	if v := c.RenderCapacityCores(); v != 0 {
		t.Fatalf("RenderCapacityCores default = %v, want 0 (autodetect)", v)
	}
	if v := c.MaxCPUPercent(); v != 0 {
		t.Fatalf("MaxCPUPercent default = %v, want 0 (disabled)", v)
	}
	if v := c.PrincipalShareDivisor(); v != 20 {
		t.Fatalf("PrincipalShareDivisor default = %v, want 20", v)
	}
	if v := c.PrincipalLRUCapacity(); v != 4096 {
		t.Fatalf("PrincipalLRUCapacity default = %v, want 4096", v)
	}
	if v := c.RenderPrincipalBurst(); v != 3 {
		t.Fatalf("RenderPrincipalBurst default = %v, want 3", v)
	}
}

func TestRenderConfigExplicitValues(t *testing.T) {
	hp := 50
	cores := 4.0
	maxCPU := 90.0
	div := 10
	cap := 512
	burst := 5
	c := &Config{Server: ServerConfig{
		RenderHeadroomPercent: &hp,
		RenderCapacityCores:   &cores,
		MaxCPUPercent:         &maxCPU,
		PrincipalShareDivisor: &div,
		RenderPrincipalBurst:  &burst,
		PrincipalLRUCapacity:  &cap,
	}}
	if h := c.RenderHeadroom(); h != 0.5 {
		t.Fatalf("RenderHeadroom(50%%) = %v, want 0.5", h)
	}
	if v := c.RenderCapacityCores(); v != 4 {
		t.Fatalf("RenderCapacityCores = %v, want 4", v)
	}
	if v := c.MaxCPUPercent(); v != 90 {
		t.Fatalf("MaxCPUPercent = %v, want 90", v)
	}
	if v := c.PrincipalShareDivisor(); v != 10 {
		t.Fatalf("PrincipalShareDivisor = %v, want 10", v)
	}
	if v := c.PrincipalLRUCapacity(); v != 512 {
		t.Fatalf("PrincipalLRUCapacity = %v, want 512", v)
	}
	if v := c.RenderPrincipalBurst(); v != 5 {
		t.Fatalf("RenderPrincipalBurst = %v, want 5", v)
	}
}

func TestLoadRejectsInvalidPrincipalLimiterSettings(t *testing.T) {
	for _, tc := range []struct {
		name, settings, want string
	}{
		{"zero divisor", "principal_share_divisor: 0\n", "server.principal_share_divisor"},
		{"negative divisor", "principal_share_divisor: -1\n", "server.principal_share_divisor"},
		{"capacity below divisor", "principal_share_divisor: 20\n  principal_lru_capacity: 19\n", "server.principal_lru_capacity"},
		{"zero burst", "render_principal_burst: 0\n", "server.render_principal_burst"},
		{"negative burst", "render_principal_burst: -1\n", "server.render_principal_burst"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			body := "auth:\n  secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\nserver:\n  " + tc.settings
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want %s", err, tc.want)
			}
		})
	}
}
