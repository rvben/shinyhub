package cli

import (
	"strings"
	"testing"
)

// TestEmitFleetManifest_EmptyServerHasCommentedTemplate verifies that scaffolding
// against an empty server still teaches the manifest format: a commented [[app]]
// block listing the supported fields, so they are discoverable without docs.
func TestEmitFleetManifest_EmptyServerHasCommentedTemplate(t *testing.T) {
	doc := emitFleetManifest("eu", "", nil)
	for _, want := range []string{
		"[[app]]",
		"slug",
		"source",
		"visibility",
		"[app.config]",
		"replicas",
		"max_sessions_per_replica",
		"hibernate_timeout_minutes",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("empty-server manifest missing %q:\n%s", want, doc)
		}
	}
}
