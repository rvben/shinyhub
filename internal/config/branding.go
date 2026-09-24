package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var hexColorRe = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

func isHTTPURL(v string) bool {
	lv := strings.ToLower(v)
	return strings.HasPrefix(lv, "http://") || strings.HasPrefix(lv, "https://")
}

// resolveLocalAsset resolves a non-URL asset reference to an absolute path and
// enforces containment within AssetsDir (symlink-aware). Returns the resolved
// absolute path.
func resolveLocalAsset(assetsDir, ref string) (string, error) {
	var abs string
	switch {
	case assetsDir == "":
		return "", fmt.Errorf("branding: %q is a local reference but no assets_dir is set", ref)
	case filepath.IsAbs(ref):
		abs = filepath.Clean(ref)
	default:
		abs = filepath.Join(assetsDir, ref)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("branding: cannot read %q: %w", ref, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("branding: %q is not a regular file", ref)
	}
	if assetsDir != "" {
		realDir, err := filepath.EvalSymlinks(assetsDir)
		if err != nil {
			return "", fmt.Errorf("branding: assets_dir %q: %w", assetsDir, err)
		}
		realFile, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", fmt.Errorf("branding: cannot resolve %q: %w", ref, err)
		}
		rel, err := filepath.Rel(realDir, realFile)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return "", fmt.Errorf("branding: %q resolves outside assets_dir", ref)
		}
	}
	return abs, nil
}

func footerSchemeOK(u string) bool {
	if strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") {
		return true
	}
	lu := strings.ToLower(u)
	for _, p := range []string{"http://", "https://", "mailto:"} {
		if strings.HasPrefix(lu, p) {
			return true
		}
	}
	return false
}

// validateBranding fails fast on any invalid branding config and populates the
// resolved-asset allow-list and landing-file path. It is a no-op when no
// branding field is set.
func validateBranding(b *BrandingConfig) error {
	switch b.RootBehavior {
	case "", "auto", "landing":
	default:
		return fmt.Errorf("branding: root_behavior %q is invalid (allowed: auto, landing)", b.RootBehavior)
	}
	if !b.IsActive() {
		return nil
	}
	if b.Theme.PrimaryColor != "" && !hexColorRe.MatchString(b.Theme.PrimaryColor) {
		return fmt.Errorf("branding: theme.primary_color %q is not a valid CSS hex color", b.Theme.PrimaryColor)
	}
	for i, fl := range b.FooterLinks {
		if strings.TrimSpace(fl.Label) == "" {
			return fmt.Errorf("branding: footer_links[%d] has an empty label", i)
		}
		if !footerSchemeOK(fl.URL) {
			return fmt.Errorf("branding: footer_links[%d] url %q uses a disallowed scheme (allowed: http, https, mailto, relative /path)", i, fl.URL)
		}
	}
	if b.AssetsDir != "" {
		info, err := os.Stat(b.AssetsDir)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("branding: assets_dir %q is not a readable directory", b.AssetsDir)
		}
	}
	b.resolvedAssets = map[string]string{}
	// resolvedAssets is keyed by served basename (the /branding/<basename> URL
	// construction in internal/ui/branding.go looks assets up the same way), so
	// two different source files that happen to share a basename would
	// otherwise silently collide: whichever resolve() call ran last wins the
	// key, and the other role serves the wrong file with no error. Logo and
	// Favicon deliberately pointing at the exact same file is fine and common
	// (one image used for both); only a same-basename, different-file collision
	// is rejected.
	resolve := func(role, ref string) (string, error) {
		abs, err := resolveLocalAsset(b.AssetsDir, ref)
		if err != nil {
			return "", err
		}
		base := filepath.Base(abs)
		if existing, ok := b.resolvedAssets[base]; ok && existing != abs {
			return "", fmt.Errorf("branding: %s %q and an earlier asset both resolve to the served name %q but are different files (%s vs %s); rename one so /branding/%s does not serve the wrong file", role, ref, base, existing, abs, base)
		}
		b.resolvedAssets[base] = abs
		return abs, nil
	}
	if b.Logo != "" && !isHTTPURL(b.Logo) {
		if _, err := resolve("logo", b.Logo); err != nil {
			return err
		}
	}
	if b.Favicon != "" && !isHTTPURL(b.Favicon) {
		if _, err := resolve("favicon", b.Favicon); err != nil {
			return err
		}
	}
	// LandingPage is intentionally NOT added to resolvedAssets: it is served via
	// the index/landing path, not the /branding/ asset handler.
	if b.LandingPage != "" {
		abs, err := resolveLocalAsset(b.AssetsDir, b.LandingPage)
		if err != nil {
			return err
		}
		b.landingFile = abs
	}
	return nil
}
