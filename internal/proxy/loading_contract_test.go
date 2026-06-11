package proxy

import (
	"os"
	"strings"
	"testing"
)

// TestLoadingPageMarkerContract pins the stable HTML element that the k6
// load-test harness uses to detect the ShinyHub loading page.
//
// Two assertions run in parallel:
//  1. The loadingPage Go constant (internal/proxy/proxy.go) contains the
//     element so the marker is never silently removed from the served HTML.
//  2. loadtest/lib.js (the k6 shared library) embeds the same literal string
//     so the harness and the server stay in sync. Either side drifting fails
//     the build instead of silently invalidating load-test results.
//
// The marker value is pinned as a literal below - do not read it from
// lib.js at test time, because the test must still catch drift when the
// file is changed. The companion value in lib.js reads:
//
//	export const LOADING_MARKER = 'id="shinyhub-box"';
const loadingMarker = `id="shinyhub-box"`

func TestLoadingPageMarkerContract(t *testing.T) {
	t.Run("go_loadingPage_const_contains_marker", func(t *testing.T) {
		if !strings.Contains(loadingPage, loadingMarker) {
			t.Errorf("loadingPage const no longer contains %q; update loadingMarker and loadtest/lib.js together", loadingMarker)
		}
	})

	t.Run("lib_js_contains_marker", func(t *testing.T) {
		// Path is relative to the package directory (internal/proxy/), so two
		// levels up reaches the repo root and then into loadtest/.
		data, err := os.ReadFile("../../loadtest/lib.js")
		if err != nil {
			t.Fatalf("cannot read loadtest/lib.js (run from repo root with GOWORK=off): %v", err)
		}
		if !strings.Contains(string(data), loadingMarker) {
			t.Errorf("loadtest/lib.js no longer contains LOADING_MARKER %q; update loadtest/lib.js and this test together", loadingMarker)
		}
	})
}
