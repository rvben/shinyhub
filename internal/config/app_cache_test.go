package config_test

import (
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

func TestAppCache_Defaults(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want, _ := filepath.Abs("./data/app-cache")
	if cfg.Storage.AppCacheDir != want {
		t.Errorf("AppCacheDir = %q, want %q", cfg.Storage.AppCacheDir, want)
	}
	if got := cfg.Storage.CacheMaxMB(); got != config.DefaultAppCacheMaxMB {
		t.Errorf("CacheMaxMB = %d, want %d", got, config.DefaultAppCacheMaxMB)
	}
	if !cfg.Storage.CacheEnabled() {
		t.Error("the result cache is off by default")
	}
}

// Zero is an explicit "off", not an unset value that falls back to the
// default: an operator who writes 0 must not get a 1 GiB cache.
func TestAppCache_ZeroDisables(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)

	cfg, err := config.Load(writeYAML(t, "storage:\n  app_cache_max_mb: 0\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Storage.CacheEnabled() || cfg.Storage.CacheMaxMB() != 0 {
		t.Fatalf("app_cache_max_mb: 0 gave enabled=%v max=%d, want off", cfg.Storage.CacheEnabled(), cfg.Storage.CacheMaxMB())
	}
}

func TestAppCache_EnvOverridesYAML(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)
	t.Setenv("SHINYHUB_APP_CACHE_DIR", "/srv/cache")
	t.Setenv("SHINYHUB_APP_CACHE_MAX_MB", "0")

	cfg, err := config.Load(writeYAML(t, "storage:\n  app_cache_dir: /elsewhere\n  app_cache_max_mb: 512\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Storage.AppCacheDir != "/srv/cache" {
		t.Errorf("AppCacheDir = %q, want the environment's /srv/cache", cfg.Storage.AppCacheDir)
	}
	if cfg.Storage.CacheEnabled() {
		t.Error("SHINYHUB_APP_CACHE_MAX_MB=0 did not override app_cache_max_mb: 512")
	}
}

func TestAppCache_InvalidSizesRejected(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, env string }{
		"negative yaml":   {yaml: "storage:\n  app_cache_max_mb: -1\n"},
		"negative env":    {env: "-5"},
		"unparseable env": {env: "1GB"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)
			if tc.env != "" {
				t.Setenv("SHINYHUB_APP_CACHE_MAX_MB", tc.env)
			}
			path := ""
			if tc.yaml != "" {
				path = writeYAML(t, tc.yaml)
			}
			if _, err := config.Load(path); err == nil {
				t.Fatal("an invalid cache size was accepted")
			}
		})
	}
}

// An unset cache root sits beside the data dir, so an install that moved its
// data (a service whose working directory is /, a container bind mount) gets a
// cache it can create without configuring a second path.
func TestAppCache_DefaultsBesideTheDataDir(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, env, want string }{
		"yaml data dir":           {yaml: "storage:\n  app_data_dir: /var/lib/shinyhub/app-data\n", want: "/var/lib/shinyhub/app-cache"},
		"env data dir":            {env: "/srv/shinyhub/app-data", want: "/srv/shinyhub/app-cache"},
		"trailing slash":          {yaml: "storage:\n  app_data_dir: /srv/x/app-data/\n", want: "/srv/x/app-cache"},
		"explicit cache dir wins": {yaml: "storage:\n  app_data_dir: /srv/x/app-data\n  app_cache_dir: /fast/cache\n", want: "/fast/cache"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SHINYHUB_AUTH_SECRET", testSecret)
			if tc.env != "" {
				t.Setenv("SHINYHUB_APP_DATA_DIR", tc.env)
			}
			path := ""
			if tc.yaml != "" {
				path = writeYAML(t, tc.yaml)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Storage.AppCacheDir != tc.want {
				t.Errorf("AppCacheDir = %q, want %q", cfg.Storage.AppCacheDir, tc.want)
			}
		})
	}
}
