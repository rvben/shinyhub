package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckReady(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		wantErr    bool
		wantDetail string
	}{
		{name: "ready", status: http.StatusOK, body: `{"ready":true}`},
		{name: "starting", status: http.StatusServiceUnavailable, body: `{"ready":false,"reason":"starting"}`, wantErr: true, wantDetail: `"reason":"starting"`},
		{name: "empty failure", status: http.StatusBadGateway, wantErr: true, wantDetail: "empty response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			err := checkReady(context.Background(), srv.Client(), srv.URL+"/readyz")
			if (err != nil) != tc.wantErr {
				t.Fatalf("checkReady() error = %v, want error %v", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), tc.wantDetail) {
				t.Fatalf("checkReady() error = %q, want detail %q", err, tc.wantDetail)
			}
		})
	}
}

func TestCheckReadyRejectsNonHTTPURL(t *testing.T) {
	err := checkReady(context.Background(), http.DefaultClient, "file:///tmp/readyz")
	if err == nil || !strings.Contains(err.Error(), "absolute http:// or https://") {
		t.Fatalf("checkReady() error = %v, want actionable URL error", err)
	}
}
