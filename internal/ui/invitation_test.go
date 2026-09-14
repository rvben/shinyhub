package ui_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/ui"
)

func TestInvitationPageIsPublicAndDoesNotCacheOrLeakReferrers(t *testing.T) {
	rec := httptest.NewRecorder()
	ui.InvitationHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/invite", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	for key, want := range map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "Content-Type": "text/html; charset=utf-8"} {
		if rec.Header().Get(key) != want {
			t.Errorf("%s: %q", key, rec.Header().Get(key))
		}
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("invitation page must not be framed")
	}
	if !strings.Contains(rec.Body.String(), `src="/static/views/accept-invitation.js"`) {
		t.Fatal("recipient controller missing")
	}
	if strings.Contains(rec.Body.String(), `src="/static/app.js"`) {
		t.Fatal("public invitation must not bootstrap authenticated routing")
	}
}
