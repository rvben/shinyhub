package main

import (
	"bytes"
	"github.com/rvben/shinyhub/internal/config"
	"image/png"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnvironmentBrandingRoutes(t *testing.T) {
	b := config.BrandingConfig{Environment: &config.EnvironmentConfig{Label: "Acceptance", Color: "#f5b301", ProductionURL: "https://production.example"}}
	mux, _ := buildBrandingMux(t, b)
	for _, path := range []string{"/", "/login", "/apps", "/unrecognised"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if !strings.Contains(w.Body.String(), "[Acceptance]") || !strings.Contains(w.Body.String(), "shinyhub-environment-loader") {
			t.Fatalf("%s missing marker", path)
		}
		if path == "/login" && !strings.Contains(w.Body.String(), `rel="apple-touch-icon"`) {
			t.Fatal("environment removed touch icon")
		}
	}
	for _, path := range []string{"/favicon.ico", "/app/.shinyhub/favicon.ico"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
			t.Fatal(w.Code, w.Header())
		}
		if _, err := png.Decode(bytes.NewReader(w.Body.Bytes())); err != nil {
			t.Fatal(err)
		}
	}
}
