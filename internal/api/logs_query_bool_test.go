package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestQueryBool(t *testing.T) {
	for _, tc := range []struct {
		raw       string
		def       bool
		want      bool
		wantError bool
	}{
		{raw: "", def: true, want: true},
		{raw: "", def: false, want: false},
		{raw: "true", def: false, want: true},
		{raw: "1", def: false, want: true},
		{raw: "false", def: true, want: false},
		{raw: "0", def: true, want: false},
		{raw: "yes", def: true, wantError: true},
		{raw: "TRUE", def: false, wantError: true},
	} {
		w := httptest.NewRecorder()
		q := url.Values{}
		if tc.raw != "" {
			q.Set("follow", tc.raw)
		}
		got, ok := queryBool(w, q, "follow", tc.def)
		if tc.wantError {
			if ok || w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "follow must be true or false") {
				t.Errorf("raw=%q: ok=%v status=%d body=%s, want a 400 naming the parameter", tc.raw, ok, w.Code, w.Body)
			}
			continue
		}
		if !ok || got != tc.want {
			t.Errorf("raw=%q default=%v: got %v ok=%v, want %v", tc.raw, tc.def, got, ok, tc.want)
		}
		if w.Body.Len() != 0 {
			t.Errorf("raw=%q: wrote a response for a valid value: %s", tc.raw, w.Body)
		}
	}
}
