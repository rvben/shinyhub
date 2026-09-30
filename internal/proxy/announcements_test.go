package proxy

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/announcementui"
)

func TestAnnouncementsInjectIndependentlyAndRespectCSP(t *testing.T) {
	p := New()
	p.SetAnnouncements(true)
	for _, policy := range []string{"", "default-src 'self'", "script-src 'none'"} {
		resp := htmlResponse("sales", testShell)
		resp.Header.Set("Content-Security-Policy", policy)
		if err := p.modifyResponseFor("sales")(resp); err != nil {
			t.Fatal(err)
		}
		body := readBody(t, resp)
		if policy == "script-src 'none'" {
			if strings.Contains(body, "shinyhub-announcements-loader") {
				t.Fatal("forbidden injection")
			}
			if resp.StatusCode != 200 {
				t.Fatal("optional notice blocked app")
			}
		} else {
			if !strings.Contains(body, "shinyhub-announcements-loader") || !strings.Contains(body, "/app/sales/.shinyhub/announcements.json") {
				t.Fatal("loader missing with app nav disabled")
			}
			if policy != "" && !strings.Contains(resp.Header.Get("Content-Security-Policy"), announcementui.CSPHash) {
				t.Fatal("missing precise CSP hash")
			}
		}
	}
	request := appPageLoad("sales")
	request.Header.Set("Accept-Encoding", "br")
	p.relaxEncodingForInjection(request)
	if request.Header.Get("Accept-Encoding") != "" {
		t.Fatal("notice-only injection cannot read response")
	}
	if page := p.decorateAppPage("<html><body>Stopped</body></html>", "sales", httptest.NewRequest("GET", "/app/sales/", nil)); !strings.Contains(page, "shinyhub-announcements-loader") {
		t.Fatal("stopped page omitted notice")
	}
}
