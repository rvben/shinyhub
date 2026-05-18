package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBrandingZeroValueIsEmpty(t *testing.T) {
	var c Config
	if c.Branding.SiteTitle != "" || c.Branding.AssetsDir != "" ||
		c.Branding.Logo != "" || c.Branding.Favicon != "" ||
		c.Branding.LandingPage != "" || c.Branding.Theme.PrimaryColor != "" ||
		len(c.Branding.FooterLinks) != 0 {
		t.Fatalf("zero-value Branding must be all-empty, got %+v", c.Branding)
	}
	if c.Branding.IsActive() {
		t.Fatal("zero-value Branding.IsActive() must be false")
	}
}

func TestBrandingIsActiveWhenAnyFieldSet(t *testing.T) {
	cases := []struct {
		name  string
		setup func(b *BrandingConfig)
	}{
		{"SiteTitle", func(b *BrandingConfig) { b.SiteTitle = "My Hub" }},
		{"AssetsDir", func(b *BrandingConfig) { b.AssetsDir = "/srv/assets" }},
		{"Logo", func(b *BrandingConfig) { b.Logo = "logo.png" }},
		{"Favicon", func(b *BrandingConfig) { b.Favicon = "favicon.ico" }},
		{"LandingPage", func(b *BrandingConfig) { b.LandingPage = "index.html" }},
		{"Theme.PrimaryColor", func(b *BrandingConfig) { b.Theme.PrimaryColor = "#ff0000" }},
		{"FooterLinks", func(b *BrandingConfig) {
			b.FooterLinks = append(b.FooterLinks, FooterLink{Label: "Docs", URL: "https://example.com"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b BrandingConfig
			tc.setup(&b)
			if !b.IsActive() {
				t.Fatalf("IsActive() must be true when %s is set", tc.name)
			}
		})
	}
}

func TestBrandingYAMLWiring(t *testing.T) {
	yaml := `
auth:
  secret: "aaaabbbbccccddddeeeeffffaaaabbbb00"
branding:
  site_title: "Acme Analytics"
  theme:
    primary_color: "#3b82f6"
  footer_links:
    - label: "Docs"
      url: "https://docs.example.com"
`
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "shinyhub.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := cfg.Branding.SiteTitle; got != "Acme Analytics" {
		t.Errorf("SiteTitle: got %q, want %q", got, "Acme Analytics")
	}
	if got := cfg.Branding.Theme.PrimaryColor; got != "#3b82f6" {
		t.Errorf("Theme.PrimaryColor: got %q, want %q", got, "#3b82f6")
	}
	if n := len(cfg.Branding.FooterLinks); n != 1 {
		t.Fatalf("FooterLinks: got %d items, want 1", n)
	}
	if got := cfg.Branding.FooterLinks[0].Label; got != "Docs" {
		t.Errorf("FooterLinks[0].Label: got %q, want %q", got, "Docs")
	}
	if got := cfg.Branding.FooterLinks[0].URL; got != "https://docs.example.com" {
		t.Errorf("FooterLinks[0].URL: got %q, want %q", got, "https://docs.example.com")
	}
	if !cfg.Branding.IsActive() {
		t.Error("IsActive() must be true after loading branding block")
	}
}

func TestBrandingEnvOverrides(t *testing.T) {
	t.Setenv("SHINYHUB_BRANDING_SITE_TITLE", "Env Title")
	t.Setenv("SHINYHUB_BRANDING_PRIMARY_COLOR", "#123abc")
	t.Setenv("SHINYHUB_BRANDING_LOGO", "https://cdn.example.com/l.svg")
	t.Setenv("SHINYHUB_BRANDING_ASSETS_DIR", "/srv/brand/assets")
	t.Setenv("SHINYHUB_BRANDING_FAVICON", "https://cdn.example.com/fav.ico")
	t.Setenv("SHINYHUB_BRANDING_LANDING_PAGE", "/srv/brand/landing.html")
	cfg := &Config{}
	applyEnv(cfg)
	if cfg.Branding.SiteTitle != "Env Title" {
		t.Errorf("SiteTitle = %q, want %q", cfg.Branding.SiteTitle, "Env Title")
	}
	if cfg.Branding.Theme.PrimaryColor != "#123abc" {
		t.Errorf("PrimaryColor = %q, want %q", cfg.Branding.Theme.PrimaryColor, "#123abc")
	}
	if cfg.Branding.Logo != "https://cdn.example.com/l.svg" {
		t.Errorf("Logo = %q, want %q", cfg.Branding.Logo, "https://cdn.example.com/l.svg")
	}
	if cfg.Branding.AssetsDir != "/srv/brand/assets" {
		t.Errorf("AssetsDir = %q, want %q", cfg.Branding.AssetsDir, "/srv/brand/assets")
	}
	if cfg.Branding.Favicon != "https://cdn.example.com/fav.ico" {
		t.Errorf("Favicon = %q, want %q", cfg.Branding.Favicon, "https://cdn.example.com/fav.ico")
	}
	if cfg.Branding.LandingPage != "/srv/brand/landing.html" {
		t.Errorf("LandingPage = %q, want %q", cfg.Branding.LandingPage, "/srv/brand/landing.html")
	}
}

func TestValidateBrandingHexColor(t *testing.T) {
	b := &BrandingConfig{Theme: ThemeConfig{PrimaryColor: "teal"}}
	if err := validateBranding(b); err == nil {
		t.Fatal("non-hex primary_color must fail validation")
	}
	b.Theme.PrimaryColor = "#0a7d8c"
	if err := validateBranding(b); err != nil {
		t.Fatalf("valid hex must pass: %v", err)
	}
}

func TestValidateBrandingFooterScheme(t *testing.T) {
	b := &BrandingConfig{FooterLinks: []FooterLink{{Label: "x", URL: "javascript:alert(1)"}}}
	if err := validateBranding(b); err == nil {
		t.Fatal("javascript: footer URL must be rejected")
	}
	b.FooterLinks = []FooterLink{
		{Label: "a", URL: "https://e.com"},
		{Label: "b", URL: "mailto:x@e.com"},
		{Label: "c", URL: "/docs"},
	}
	if err := validateBranding(b); err != nil {
		t.Fatalf("allowed schemes must pass: %v", err)
	}
	// Schemes are case-insensitive per RFC 3986.
	b.FooterLinks = []FooterLink{{Label: "x", URL: "HTTP://x.example.com"}}
	if err := validateBranding(b); err != nil {
		t.Fatalf("uppercase HTTP scheme must be accepted: %v", err)
	}
}

func TestValidateBrandingAssetContainmentAndResolution(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "logo.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "home.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &BrandingConfig{AssetsDir: dir, Logo: "logo.svg", LandingPage: "home.html"}
	if err := validateBranding(b); err != nil {
		t.Fatalf("valid assets must pass: %v", err)
	}
	if b.resolvedAssets["logo.svg"] != filepath.Join(dir, "logo.svg") {
		t.Fatalf("logo not resolved: %+v", b.resolvedAssets)
	}
	if b.landingFile != filepath.Join(dir, "home.html") {
		t.Fatalf("landing not resolved: %q", b.landingFile)
	}

	// Dotdot escape via an existing sibling file must be caught by containment,
	// not merely by os.Stat failing.
	parent := filepath.Dir(dir)
	siblingFile := filepath.Join(parent, "outside.txt")
	if err := os.WriteFile(siblingFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	siblingRef := "../" + filepath.Base(siblingFile)
	if err := validateBranding(&BrandingConfig{AssetsDir: dir, Logo: siblingRef}); err == nil {
		t.Fatal("dotdot escape to existing sibling file must be rejected by containment")
	}

	// Symlink inside assets_dir pointing outside must also be rejected.
	outsideFile := filepath.Join(parent, "secret.svg")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(dir, "evil.svg")
	if symlinkErr := os.Symlink(outsideFile, symlinkPath); symlinkErr != nil {
		if os.IsPermission(symlinkErr) {
			t.Skip("cannot create symlinks on this system; skipping symlink-escape subcase")
		}
		t.Fatalf("os.Symlink: %v", symlinkErr)
	}
	if err := validateBranding(&BrandingConfig{AssetsDir: dir, Logo: "evil.svg"}); err == nil {
		t.Fatal("symlink escaping assets_dir must be rejected by EvalSymlinks containment")
	}

	// Missing file must fail.
	miss := &BrandingConfig{AssetsDir: dir, LandingPage: "nope.html"}
	if err := validateBranding(miss); err == nil {
		t.Fatal("missing landing_page must fail fast")
	}

	// Any local ref (relative or absolute) without assets_dir must fail.
	norel := &BrandingConfig{Logo: "logo.svg"}
	if err := validateBranding(norel); err == nil {
		t.Fatal("relative asset ref with no assets_dir must fail")
	}
	noabs := &BrandingConfig{Logo: filepath.Join(dir, "logo.svg")}
	if err := validateBranding(noabs); err == nil {
		t.Fatal("absolute asset ref with no assets_dir must fail")
	}

	// URL logo needs no assets_dir.
	urlonly := &BrandingConfig{Logo: "https://cdn.example.com/l.svg", SiteTitle: "x"}
	if err := validateBranding(urlonly); err != nil {
		t.Fatalf("URL logo without assets_dir must pass: %v", err)
	}
	// URL scheme matching is case-insensitive.
	urlUpper := &BrandingConfig{Logo: "HTTPS://cdn.example.com/l.svg", SiteTitle: "x"}
	if err := validateBranding(urlUpper); err != nil {
		t.Fatalf("uppercase HTTPS URL logo must pass: %v", err)
	}
}
