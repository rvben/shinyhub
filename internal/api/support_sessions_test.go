package api_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func newSupportSessionServer(t *testing.T, enabled bool) (*api.Server, *db.Store, *db.User, *db.User) {
	t.Helper()
	store := dbtest.New(t)
	_, adminID := seedUserAndJWT(t, store, "support-admin", "admin")
	_, subjectID := seedUserAndJWT(t, store, "support-alice", "viewer")
	admin, _ := store.GetUserByID(adminID)
	subject, _ := store.GetUserByID(subjectID)
	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "sales", Name: "Sales", ProjectSlug: "default", OwnerID: admin.ID, Access: "public",
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret", SupportSessions: enabled},
		Server:  config.ServerConfig{BaseURL: "https://hub.example.com", AppOrigin: "https://apps.example.com"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	return api.New(cfg, store, nil, nil), store, admin, subject
}

func TestCreateSupportSessionReturnsSingleUseAppCapability(t *testing.T) {
	srv, store, admin, subject := newSupportSessionServer(t, true)
	token, _ := auth.IssueJWT(admin.ID, admin.Username, admin.Role, "test-secret")
	body, _ := json.Marshal(map[string]any{
		"user_id": subject.ID, "app_slug": "sales", "reason": "Investigating ticket SUP-1042",
	})
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, http.MethodPost, "/api/support-sessions", body, token))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		ID        string    `json:"id"`
		LaunchURL string    `json:"launch_url"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	launch, err := url.Parse(response.LaunchURL)
	if err != nil || launch.Host != "apps.example.com" || launch.Path != "/app/sales/" {
		t.Fatalf("launch URL = %q (%v)", response.LaunchURL, err)
	}
	raw := launch.Query().Get("__shinyhub_launch")
	if response.ID == "" || raw == "" || time.Until(response.ExpiresAt) > 15*time.Minute || time.Until(response.ExpiresAt) < 14*time.Minute {
		t.Fatalf("unexpected response: %+v", response)
	}
	sum := sha256.Sum256([]byte(raw))
	impersonated, err := store.ConsumeAppLaunchCode(hex.EncodeToString(sum[:]), "sales")
	if err != nil {
		t.Fatal(err)
	}
	if impersonated.ID != subject.ID || impersonated.SupportSession == nil ||
		impersonated.SupportSession.ActorID != admin.ID || impersonated.SupportSession.AppSlug != "sales" {
		t.Fatalf("dual principal = %+v", impersonated)
	}
	if _, err := store.ConsumeAppLaunchCode(hex.EncodeToString(sum[:]), "sales"); err == nil {
		t.Fatal("launch capability replay should fail")
	}
	events, err := store.ListAuditEvents("support_session.start", 10, 0)
	if err != nil || len(events) != 1 || events[0].ResourceID != response.ID ||
		!strings.Contains(events[0].Detail, "Investigating ticket SUP-1042") || strings.Contains(events[0].Detail, raw) {
		t.Fatalf("start audit events=%+v err=%v", events, err)
	}
}

func TestCreateSupportSessionIsOptInAndRequiresRecentAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name       string
		enabled    bool
		authTime   time.Time
		wantStatus int
	}{
		{name: "disabled", enabled: false, authTime: time.Now(), wantStatus: http.StatusNotFound},
		{name: "stale auth", enabled: true, authTime: time.Now().Add(-11 * time.Minute), wantStatus: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, admin, subject := newSupportSessionServer(t, tc.enabled)
			token, _ := auth.IssueJWTAt(admin.ID, admin.Username, admin.Role, "test-secret", tc.authTime)
			body, _ := json.Marshal(map[string]any{"user_id": subject.ID, "app_slug": "sales", "reason": "Investigating support ticket"})
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, authedRequest(t, http.MethodPost, "/api/support-sessions", body, token))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestCreateSupportSessionRejectsPrivilegedTargetsAndSelf(t *testing.T) {
	for _, role := range []string{"operator", "admin"} {
		t.Run(role, func(t *testing.T) {
			srv, store, admin, _ := newSupportSessionServer(t, true)
			_, targetID := seedUserAndJWT(t, store, "target-"+role, role)
			token, _ := auth.IssueJWT(admin.ID, admin.Username, admin.Role, "test-secret")
			body, _ := json.Marshal(map[string]any{"user_id": targetID, "app_slug": "sales", "reason": "Investigating support ticket"})
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, authedRequest(t, http.MethodPost, "/api/support-sessions", body, token))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
	t.Run("self", func(t *testing.T) {
		srv, _, admin, _ := newSupportSessionServer(t, true)
		token, _ := auth.IssueJWT(admin.ID, admin.Username, admin.Role, "test-secret")
		body, _ := json.Marshal(map[string]any{"user_id": admin.ID, "app_slug": "sales", "reason": "Investigating support ticket"})
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, authedRequest(t, http.MethodPost, "/api/support-sessions", body, token))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestListSupportSessionAppsReturnsOnlySubjectVisibleApps(t *testing.T) {
	srv, store, admin, subject := newSupportSessionServer(t, true)
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "visible-private", Name: "Visible", OwnerID: admin.ID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "hidden-private", Name: "Hidden", OwnerID: admin.ID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	if err := store.GrantAppAccess("visible-private", subject.ID); err != nil {
		t.Fatal(err)
	}
	token, _ := auth.IssueJWT(admin.ID, admin.Username, admin.Role, "test-secret")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, http.MethodGet, "/api/users/"+strconv.FormatInt(subject.ID, 10)+"/support-apps", nil, token))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Items []struct {
			Slug string `json:"slug"`
		} `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, item := range response.Items {
		slugs = append(slugs, item.Slug)
	}
	joined := strings.Join(slugs, ",")
	if !strings.Contains(joined, "sales") || !strings.Contains(joined, "visible-private") || strings.Contains(joined, "hidden-private") {
		t.Fatalf("eligible apps = %v", slugs)
	}
}
