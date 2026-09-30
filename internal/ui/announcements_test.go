package ui_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/rvben/shinyhub/internal/announcementui"
	"github.com/rvben/shinyhub/internal/ui"
)

func TestAnnouncementLoaderSurvivesAssetVersioning(t *testing.T) {
	shell, err := ui.ShellHTML()
	if err != nil {
		t.Fatal(err)
	}
	loader := regexp.MustCompile(`<script\b[^>]*\bid="shinyhub-announcements-loader"[^>]*\bsrc="([^"]+)"`).FindSubmatch(shell)
	if len(loader) != 2 {
		t.Fatal("served shell has no announcement loader")
	}
	// Exercise the URL actually emitted by the production shell, which rewrites
	// embedded static assets but must preserve this separately served client.
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+announcementui.ClientPath, announcementui.ScriptHandler)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, string(loader[1]), nil))
	if rec.Code != http.StatusOK || rec.Body.String() != announcementui.Script {
		t.Fatalf("shell announcement URL %q is unreachable: status %d", loader[1], rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatal("shared client must revalidate across upgrades")
	}
}
