package proxy

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
)

func TestBrowserDeadlineUsesOriginalLoginNotRenewableTokenExpiry(t *testing.T) {
	p := New()
	p.SetBrowserSessions(12*time.Hour, "https://hub.example.com")
	r := httptest.NewRequest("GET", "/app/sales/", nil)
	u := &auth.ContextUser{ID: 9, Role: "viewer"}
	original := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	ctx := auth.WithUser(r.Context(), u)
	ctx = auth.WithTokenInfo(ctx, &auth.TokenInfo{AuthTime: original, ExpiresAt: time.Now().Add(time.Minute)})
	r = r.WithContext(ctx)
	if got := p.browserSessionDeadline(r); !got.Equal(original.Add(12 * time.Hour)) {
		t.Fatalf("deadline = %v", got)
	}
	script := p.browserPageScript(r, "sales")
	if script == nil || !strings.Contains(script.snippet, `data-session-url="/app/sales/.shinyhub/session.json"`) || !strings.Contains(script.snippet, `data-sign-in-url="https://hub.example.com/app/sales/"`) {
		t.Fatalf("missing app-local renewal without nav/overlay: %+v", script)
	}
	u.SupportSession = &auth.SupportSessionContext{}
	if p.browserPageScript(r, "sales") != nil || !p.browserSessionDeadline(r).IsZero() {
		t.Fatal("support identity received browser renewal/deadline")
	}
	u.SupportSession = nil
	r = r.WithContext(auth.WithTokenInfo(r.Context(), nil))
	if !p.browserSessionDeadline(r).IsZero() {
		t.Fatal("forward-auth acquired native browser deadline")
	}
}
