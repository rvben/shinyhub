package proxy

import (
	"fmt"
	"os"
	"regexp"
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

// elementIDPattern matches the getElementById calls a wait script makes.
var elementIDPattern = regexp.MustCompile(`getElementById\('([^']+)'\)`)

// TestWaitPagesExposeEveryIDTheirScriptsUse asserts that every element a wait
// script reaches for actually exists in the page that carries it.
//
// The pages are assembled from two pieces written far apart: waitPage() builds
// the HTML shell, and each script addresses that shell by id. Nothing in Go
// links them, so renaming an id in the shell leaves the script compiling,
// shipping, and throwing on the user's first paint - which on these pages means
// no spinner, no give-up state, and no retry button, i.e. a dead tab on exactly
// the requests that were already going badly.
//
// It is also what keeps internal/ui/jstests/wait-page-scripts.test.js honest:
// that test drives the real script text against a fixture DOM, and this
// assertion is the guarantee that the fixture's ids are the shipped page's ids.
func TestWaitPagesExposeEveryIDTheirScriptsUse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		page   string
	}{
		{"loading", loadingScript, loadingPage},
		{"deploying", deployingScript, deployingPage},
		{"waiting", waitingScript, waitingPage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matches := elementIDPattern.FindAllStringSubmatch(tc.script, -1)
			for _, m := range matches {
				if attr := fmt.Sprintf("id=%q", m[1]); !strings.Contains(tc.page, attr) {
					t.Errorf("%s script reads element %q but the page has no %s; the script will throw on load",
						tc.name, m[1], attr)
				}
			}
			// deployingScript touches no elements, so an empty match set is
			// legitimate there and only there. Anywhere else it means the
			// pattern stopped matching and every assertion above was skipped.
			if len(matches) == 0 && tc.name != "deploying" {
				t.Fatalf("%s script matched no getElementById calls; elementIDPattern no longer matches the script and this test proves nothing", tc.name)
			}
		})
	}
}
