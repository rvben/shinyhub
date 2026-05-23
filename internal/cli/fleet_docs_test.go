package cli

import (
	"os"
	"strings"
	"testing"
)

// TestManifestDocCrossLinksFleetPrecedence guards the cross-link from the bundle
// manifest doc to the fleet precedence section. The bundle `shinyhub.toml` is
// only the per-deploy layer; a fleet manifest overrides it. If manifest.md
// drops the link, or fleet.md renames the "## Config precedence" heading the
// link targets, readers lose the precedence story and the anchor breaks
// silently. This test fails on either regression.
func TestManifestDocCrossLinksFleetPrecedence(t *testing.T) {
	manifest, err := os.ReadFile("../../docs/manifest.md")
	if err != nil {
		t.Fatalf("read manifest.md: %v", err)
	}
	fleet, err := os.ReadFile("../../docs/fleet.md")
	if err != nil {
		t.Fatalf("read fleet.md: %v", err)
	}

	if !strings.Contains(string(manifest), "fleet.md#config-precedence") {
		t.Error("manifest.md must link to fleet.md#config-precedence so readers find the precedence story")
	}
	if !strings.Contains(string(manifest), "fleet manifest > bundle") {
		t.Error("manifest.md must state the precedence order (fleet manifest > bundle shinyhub.toml > server default)")
	}
	// The anchor "#config-precedence" resolves to a "## Config precedence"
	// heading; assert it still exists so the cross-link does not dangle.
	if !strings.Contains(string(fleet), "## Config precedence") {
		t.Error("fleet.md must keep the '## Config precedence' heading that manifest.md links to")
	}
}
