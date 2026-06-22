package ui_test

import (
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"regexp"
	"testing"

	"github.com/rvben/shinyhub/internal/ui"
)

// Bodiless <script>/<style> tags only: the SPA's external scripts are
// <script src=... defer></script> (attributes after the name), so these match
// exactly the inline blocks RenderIndex injects.
var (
	inlineScriptRe = regexp.MustCompile(`(?s)<script>(.*?)</script>`)
	inlineStyleRe  = regexp.MustCompile(`(?s)<style>(.*?)</style>`)
)

// TestCSPInlineSources_CoversRenderedInline is the drift guard for the strict
// CSP: every inline <script>/<style> in the branded shell must be allowed by a
// hash that CSPInlineSources emits. If RenderIndex's inline format changes, or
// the shell gains a new inline block, this fails until the CSP covers it again,
// so a strict CSP can never silently start blocking the dashboard.
func TestCSPInlineSources_CoversRenderedInline(t *testing.T) {
	p := ui.Public{
		SiteTitle:    "Acme Analytics",
		PrimaryColor: "#1a2b3c",
		Favicon:      "/branding/favicon.png",
		Logo:         "/branding/logo.svg",
	}
	raw, err := fs.ReadFile(ui.Static(), "index.html")
	if err != nil {
		t.Fatalf("read shell: %v", err)
	}
	out, err := ui.RenderIndex(raw, p)
	if err != nil {
		t.Fatalf("RenderIndex: %v", err)
	}
	scriptSrc, styleSrc, err := ui.CSPInlineSources(p)
	if err != nil {
		t.Fatalf("CSPInlineSources: %v", err)
	}

	assertCovered(t, "script", inlineScriptRe.FindAllSubmatch(out, -1), scriptSrc)
	assertCovered(t, "style", inlineStyleRe.FindAllSubmatch(out, -1), styleSrc)
}

func assertCovered(t *testing.T, kind string, matches [][][]byte, sources []string) {
	t.Helper()
	if len(matches) == 0 {
		t.Fatalf("no inline <%s> found in rendered shell; the coverage check is vacuous", kind)
	}
	for _, m := range matches {
		sum := sha256.Sum256(m[1])
		want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		found := false
		for _, s := range sources {
			if s == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("inline <%s> body %q -> %s is not in CSP sources %v (a strict CSP would block it)", kind, m[1], want, sources)
		}
	}
}
