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
