package api

import (
	"encoding/json"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"net/http/httptest"
	"testing"
)

func TestEnvironmentMetadata(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		b := config.BrandingConfig{}
		if enabled {
			b.Environment = &config.EnvironmentConfig{Label: "Acceptance", Color: "#abc", Message: "Test data"}
		}
		s, _ := newBrandingTestServer(t, b)
		for _, path := range []string{"/api/server-info", "/api/auth/me", "/.shinyhub/branding.json"} {
			r := reqWithOptionalUser("GET", path, &auth.ContextUser{ID: 123, Username: "viewer", Role: "viewer"})
			w := httptest.NewRecorder()
			switch path {
			case "/api/server-info":
				s.handleServerInfo(w, r)
			case "/api/auth/me":
				s.handleMe(w, r)
			default:
				s.HandleBrandingJSON(w, r)
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if path == "/.shinyhub/branding.json" {
				_, present := body["environment"]
				if present != enabled {
					t.Fatal(body)
				}
			} else {
				label, present := body["environment_label"]
				if present != enabled || (enabled && label != "Acceptance") {
					t.Fatal(body)
				}
			}
		}
	}
}
