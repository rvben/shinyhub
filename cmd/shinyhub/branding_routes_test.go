package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/ui"
)

// buildBrandingMux constructs a minimal mux by calling the production
// registerBrandingRoutes function directly. This guarantees the test exercises
// exactly the same route wiring as runServe, not a copy of it.
func buildBrandingMux(t *testing.T, branding config.BrandingConfig) (*http.ServeMux, *db.Store) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	cfg := &config.Config{
		Auth:     config.AuthConfig{Secret: "test-secret"},
		Storage:  config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
		Branding: branding,
	}
	srv := api.New(cfg, store, nil, nil)

	appUserLookup := func(id int64) (*auth.ContextUser, error) {
		u, err := store.GetUserByID(id)
		if err != nil {
			return nil, err
		}
		return &auth.ContextUser{ID: u.ID, Username: u.Username, Role: u.Role}, nil
	}

	mux := http.NewServeMux()
	registerBrandingRoutes(mux, cfg, srv, store, appUserLookup)
	return mux, store
}

// TestBrandingRoutes is the integration test for the branding route wiring.
func TestBrandingRoutes(t *testing.T) {
	// Reference bytes from the real ServeFileFS path, used for byte-identity
	// assertions.
	staticIndex := mustReadStaticIndex(t)

	t.Run("zero_branding_root_byte_identical", func(t *testing.T) {
		mux, _ := buildBrandingMux(t, config.BrandingConfig{})

		// Serve via the mux
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))

		if rr.Code != http.StatusOK {
			t.Fatalf("GET / status = %d, want 200", rr.Code)
		}

		// Byte-identical to the static index.html via ServeFileFS: this proves
		// the zero-branding route delegates to the same ServeFileFS call and
		// does not run through any hand-written writer that could alter bytes.
		body := rr.Body.Bytes()
		if !bytes.Equal(body, staticIndex) {
			t.Errorf("GET / body not byte-identical to ui.Static()/index.html (got %d bytes, want %d bytes)",
				len(body), len(staticIndex))
		}

		// Content-Type must be text/html (set by ServeFileFS via sniff).
		ct := rr.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET / Content-Type = %q, want text/html prefix", ct)
		}

		// Accept-Ranges: bytes is set by ServeFileFS but not by a hand-written
		// fs.ReadFile + w.Write path. This proves the zero-branding route still
		// goes through ServeFileFS and the backwards-compat invariant holds.
		if rr.Header().Get("Accept-Ranges") != "bytes" {
			t.Errorf("GET / missing Accept-Ranges: bytes; ServeFileFS path may have regressed")
		}
	})

	t.Run("zero_branding_login_serves_spa_shell", func(t *testing.T) {
		mux, _ := buildBrandingMux(t, config.BrandingConfig{})

		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest("GET", "/login", nil))

		if rr.Code != http.StatusOK {
			t.Fatalf("GET /login status = %d, want 200", rr.Code)
		}

		// The SPA shell must reference the compiled app bundle.
		body := rr.Body.String()
		if !strings.Contains(body, "app.js") {
			t.Error("GET /login body does not contain 'app.js': may not be serving the SPA shell")
		}

		// Accept-Ranges: bytes proves the zero-branding /login route still goes
		// through ServeFileFS, not a hand-written writer.
		if rr.Header().Get("Accept-Ranges") != "bytes" {
			t.Errorf("GET /login missing Accept-Ranges: bytes; ServeFileFS path may have regressed")
		}
	})

	t.Run("branding_active_no_landing_root_injects_branding", func(t *testing.T) {
		branding := config.BrandingConfig{SiteTitle: "AcmeCorp"}
		mux, _ := buildBrandingMux(t, branding)

		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))

		if rr.Code != http.StatusOK {
			t.Fatalf("GET / status = %d, want 200", rr.Code)
		}

		body := rr.Body.String()
		if !strings.Contains(body, "window.__SHINYHUB_BRANDING__") {
			t.Error("GET / with active branding does not contain window.__SHINYHUB_BRANDING__")
		}
	})

	t.Run("branding_active_with_landing_page_root_returns_landing_bytes", func(t *testing.T) {
		// Create a real landing page file.
		landingContent := []byte("<!DOCTYPE html><html><body>operator landing</body></html>")
		landingPath := filepath.Join(t.TempDir(), "landing.html")
		if err := os.WriteFile(landingPath, landingContent, 0o644); err != nil {
			t.Fatal(err)
		}

		// Since config.Load() resolves LandingPage -> landingFile, we build the
		// config via Load() to get a properly resolved BrandingConfig with a
		// non-empty LandingFile().
		cfg := buildBrandingConfigWithLanding(t, landingPath)
		mux, _ := buildBrandingMux(t, cfg)

		// GET / must return the raw operator file.
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))

		if rr.Code != http.StatusOK {
			t.Fatalf("GET / status = %d, want 200", rr.Code)
		}

		body := rr.Body.Bytes()
		if !bytes.Equal(body, landingContent) {
			t.Errorf("GET / body = %q, want raw landing file bytes %q", body, landingContent)
		}
	})

	t.Run("branding_active_with_landing_page_login_still_spa", func(t *testing.T) {
		landingContent := []byte("<!DOCTYPE html><html><body>operator landing</body></html>")
		landingPath := filepath.Join(t.TempDir(), "landing.html")
		if err := os.WriteFile(landingPath, landingContent, 0o644); err != nil {
			t.Fatal(err)
		}

		cfg := buildBrandingConfigWithLanding(t, landingPath)
		mux, _ := buildBrandingMux(t, cfg)

		// GET /login must still serve the SPA shell, NOT the operator file.
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest("GET", "/login", nil))

		if rr.Code != http.StatusOK {
			t.Fatalf("GET /login status = %d, want 200", rr.Code)
		}

		body := rr.Body.Bytes()
		if bytes.Equal(body, landingContent) {
			t.Error("GET /login returned the landing page; it must always serve the SPA shell")
		}
		if !strings.Contains(string(body), "app.js") {
			t.Error("GET /login body does not contain 'app.js': may not be serving the SPA shell")
		}
	})

	t.Run("branding_asset_handler_serves_logo", func(t *testing.T) {
		// Create a logo file in a temp dir.
		assetsDir := t.TempDir()
		logoContent := []byte("<svg>logo</svg>")
		if err := os.WriteFile(filepath.Join(assetsDir, "logo.svg"), logoContent, 0o644); err != nil {
			t.Fatal(err)
		}

		cfg := buildBrandingConfigWithAssets(t, assetsDir)
		mux, _ := buildBrandingMux(t, cfg)

		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest("GET", "/branding/logo.svg", nil))

		if rr.Code != http.StatusOK {
			t.Fatalf("GET /branding/logo.svg status = %d, want 200", rr.Code)
		}

		body := rr.Body.Bytes()
		if !bytes.Equal(body, logoContent) {
			t.Errorf("GET /branding/logo.svg body = %q, want %q", body, logoContent)
		}
	})

	t.Run("shinyhub_branding_json_returns_200", func(t *testing.T) {
		branding := config.BrandingConfig{SiteTitle: "TestHub"}
		mux, _ := buildBrandingMux(t, branding)

		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest("GET", "/.shinyhub/branding.json", nil))

		if rr.Code != http.StatusOK {
			t.Fatalf("GET /.shinyhub/branding.json status = %d, want 200", rr.Code)
		}
	})

	t.Run("shinyhub_apps_json_returns_200", func(t *testing.T) {
		mux, _ := buildBrandingMux(t, config.BrandingConfig{})

		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest("GET", "/.shinyhub/apps.json", nil))

		if rr.Code != http.StatusOK {
			t.Fatalf("GET /.shinyhub/apps.json status = %d, want 200", rr.Code)
		}
	})

	t.Run("spa_ui_routes_serve_shell", func(t *testing.T) {
		mux, _ := buildBrandingMux(t, config.BrandingConfig{})

		paths := []string{
			"/apps/replica-smoke",
			"/apps/replica-smoke/logs",
			"/users",
			"/audit-log",
		}
		for _, path := range paths {
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))

			if rr.Code != http.StatusOK {
				t.Errorf("GET %s status = %d, want 200", path, rr.Code)
				continue
			}
			if !strings.Contains(rr.Body.String(), "app.js") {
				t.Errorf("GET %s body does not contain 'app.js': may not be serving the SPA shell", path)
			}
		}
	})

	t.Run("unknown_paths_404", func(t *testing.T) {
		mux, _ := buildBrandingMux(t, config.BrandingConfig{})

		// /apps (bare, no trailing slash) is excluded: Go's ServeMux issues a
		// 301 redirect to /apps/ when the /apps/ subtree pattern is registered,
		// so it returns 3xx, not 404. The paths below are those that truly reach
		// the catch-all "/" handler (or the spa handler with IsUIPath false) and
		// must return 404.
		paths := []string{
			"/nope",
			"/apps/",
			"/favicon.ico",
		}
		for _, path := range paths {
			t.Run(path, func(t *testing.T) {
				rr := httptest.NewRecorder()
				mux.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))

				if rr.Code != http.StatusNotFound {
					t.Errorf("GET %s status = %d, want 404", path, rr.Code)
				}
			})
		}
	})
}

// mustReadStaticIndex reads index.html directly from the embedded FS using the
// same ServeFileFS path the zero-branding route uses.
func mustReadStaticIndex(t *testing.T) []byte {
	t.Helper()
	// Serve via a real httptest round-trip using ServeFileFS so we get the same
	// bytes (after any ServeFileFS compression/range negotiation). For the
	// byte-identity comparison we use a plain GET with no special headers, which
	// mirrors the test request in the main case.
	rr := httptest.NewRecorder()
	http.ServeFileFS(rr, httptest.NewRequest("GET", "/", nil), ui.Static(), "index.html")
	if rr.Code != http.StatusOK {
		t.Fatalf("mustReadStaticIndex: ServeFileFS returned %d", rr.Code)
	}
	return rr.Body.Bytes()
}

// buildBrandingConfigWithLanding creates a BrandingConfig whose LandingFile()
// returns landingPath. Because landingFile is an unexported field populated by
// config.Load(), we use a minimal YAML file and invoke config.Load() to get a
// properly resolved BrandingConfig.
//
// The landing page must be inside assetsDir; config validation requires an
// assets_dir when landing_page is a local file path.
func buildBrandingConfigWithLanding(t *testing.T, landingPath string) config.BrandingConfig {
	t.Helper()
	assetsDir := filepath.Dir(landingPath)
	cfgDir := t.TempDir()
	dbPath := filepath.Join(cfgDir, "shinyhub.db")
	appsDir := filepath.Join(cfgDir, "apps")
	appDataDir := filepath.Join(cfgDir, "appdata")
	// landing_page validation requires assets_dir when the ref is a local path.
	yaml := "auth:\n  secret: test-secret-for-config-load-minimum32\ndatabase:\n  dsn: " + dbPath + "\nstorage:\n  apps_dir: " + appsDir + "\n  app_data_dir: " + appDataDir + "\nbranding:\n  site_title: AcmeCorp\n  assets_dir: " + assetsDir + "\n  landing_page: " + landingPath + "\n"
	cfgPath := filepath.Join(cfgDir, "shinyhub.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg.Branding
}

// buildBrandingConfigWithAssets creates a BrandingConfig with an assets_dir
// pointing to assetsDir, using config.Load() to populate resolvedAssets.
// The logo field is set so that logo.svg gets into the resolved-assets allow-list.
func buildBrandingConfigWithAssets(t *testing.T, assetsDir string) config.BrandingConfig {
	t.Helper()
	cfgDir := t.TempDir()
	dbPath := filepath.Join(cfgDir, "shinyhub.db")
	appsDir := filepath.Join(cfgDir, "apps")
	appDataDir := filepath.Join(cfgDir, "appdata")
	// Set logo to "logo.svg" (relative to assets_dir) so it gets added to
	// resolvedAssets and the /branding/ handler is registered.
	yaml := "auth:\n  secret: test-secret-for-config-load-minimum32\ndatabase:\n  dsn: " + dbPath + "\nstorage:\n  apps_dir: " + appsDir + "\n  app_data_dir: " + appDataDir + "\nbranding:\n  assets_dir: " + assetsDir + "\n  logo: logo.svg\n"
	cfgPath := filepath.Join(cfgDir, "shinyhub.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg.Branding
}
