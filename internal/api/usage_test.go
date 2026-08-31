package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/usage"
)

type usageAPIResponse struct {
	Enabled                bool                    `json:"enabled"`
	WindowDays             int                     `json:"window_days"`
	RawRetentionDays       int                     `json:"raw_retention_days"`
	AggregateRetentionDays int                     `json:"aggregate_retention_days"`
	IdentityMode           string                  `json:"identity_mode"`
	IdentityDetail         bool                    `json:"identity_detail"`
	Summary                db.UsageSummary         `json:"summary"`
	Viewers                []db.UsageViewer        `json:"viewers"`
	Recent                 []db.UsageRecentSession `json:"recent_sessions"`
}

func TestAppUsagePrivacyDowngradeCommitsPolicyAndScrubsBeforeSuccess(t *testing.T) {
	srv, store := newTestServer(t)
	srv.Config().Usage = config.UsageConfig{Enabled: true, IdentityMode: config.UsageIdentityIdentified, RawRetentionDays: 30, AggregateRetentionDays: 365}
	policy, err := usage.LoadOrInitPolicy(store, config.UsageIdentityIdentified, srv.Config().Auth.Secret)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetUsagePolicy(policy)
	ownerToken, ownerID := seedUserAndJWT(t, store, "usage-privacy-owner", "developer")
	_, viewerID := seedUserAndJWT(t, store, "usage-privacy-viewer", "viewer")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "usage-privacy", Name: "Usage privacy", OwnerID: ownerID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("usage-privacy")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BeginUsageSession(db.UsageSessionStart{
		ID: "named-before-downgrade", Slug: app.Slug, UserID: viewerID,
		PrincipalKind: "person", IdentityMode: "identified", InstanceID: "cp",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, http.MethodPatch, "/api/apps/usage-privacy", []byte(`{"usage_identity_mode":"unattributed"}`), ownerToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", rec.Code, rec.Body.String())
	}
	var identifierRows int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM usage_sessions WHERE app_id = ? AND (user_id IS NOT NULL OR viewer_key IS NOT NULL)`, app.ID).Scan(&identifierRows); err != nil {
		t.Fatal(err)
	}
	if identifierRows != 0 {
		t.Fatalf("privacy downgrade retained %d identifying rows", identifierRows)
	}
	got, err := store.GetAppBySlug(app.Slug)
	if err != nil || got.UsageIdentityMode == nil || *got.UsageIdentityMode != "unattributed" {
		t.Fatalf("stored app privacy mode=%v err=%v", got.UsageIdentityMode, err)
	}
}

func getUsage(t *testing.T, srv interface{ Router() http.Handler }, path, token string) (*httptest.ResponseRecorder, usageAPIResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, http.MethodGet, path, nil, token))
	var body usageAPIResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode usage response: %v", err)
		}
	}
	return rec, body
}

func TestAppUsageSeparatesManagerAggregatesFromAdminIdentity(t *testing.T) {
	srv, store := newTestServer(t)
	srv.Config().Usage = config.UsageConfig{Enabled: true, IdentityMode: config.UsageIdentityIdentified, RawRetentionDays: 30, AggregateRetentionDays: 90}
	ownerToken, ownerID := seedUserAndJWT(t, store, "usage-owner", "developer")
	adminToken, _ := seedUserAndJWT(t, store, "usage-admin", "admin")
	_, viewerID := seedUserAndJWT(t, store, "usage-person", "viewer")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "usage-api", Name: "Usage", OwnerID: ownerID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginUsageSession(db.UsageSessionStart{
		ID: "api-session", Slug: "usage-api", UserID: viewerID, InstanceID: "cp",
		StartedAt: time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	rec, manager := getUsage(t, srv, "/api/apps/usage-api/usage?days=7", ownerToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("manager Cache-Control=%q, want no-store", got)
	}
	if manager.WindowDays != 7 || manager.RawRetentionDays != 30 || manager.AggregateRetentionDays != 90 || manager.IdentityMode != "identified" || manager.Summary.Sessions != 1 || manager.Summary.PeakConcurrentSessions != 1 {
		t.Fatalf("manager response = %+v", manager)
	}
	if manager.IdentityDetail || len(manager.Viewers) != 0 || len(manager.Recent) != 0 {
		t.Fatal("app manager received administrator identity detail")
	}

	rec, admin := getUsage(t, srv, "/api/apps/usage-api/usage?days=30", adminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("admin Cache-Control=%q, want no-store", got)
	}
	if !admin.IdentityDetail || len(admin.Viewers) != 1 || admin.Viewers[0].UserID != viewerID || len(admin.Recent) != 1 {
		t.Fatalf("admin identity response = %+v", admin)
	}
}

func TestAppUsageAuthorizationValidationAndDisabledState(t *testing.T) {
	srv, store := newTestServer(t)
	srv.Config().Usage = config.UsageConfig{Enabled: true, IdentityMode: config.UsageIdentityUnattributed, RawRetentionDays: 7, AggregateRetentionDays: 14}
	ownerToken, ownerID := seedUserAndJWT(t, store, "usage-owner-two", "developer")
	readerToken, readerID := seedUserAndJWT(t, store, "usage-reader", "viewer")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "usage-guard", Name: "Usage", OwnerID: ownerID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	if err := store.GrantAppAccess("usage-guard", readerID); err != nil {
		t.Fatal(err)
	}

	rec, _ := getUsage(t, srv, "/api/apps/usage-guard/usage", readerToken)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("reader status=%d, want 403", rec.Code)
	}
	rec, _ = getUsage(t, srv, "/api/apps/usage-guard/usage?days=8", ownerToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid window status=%d, want 400", rec.Code)
	}
	rec, retained := getUsage(t, srv, "/api/apps/usage-guard/usage?days=365", ownerToken)
	if rec.Code != http.StatusOK || retained.WindowDays != 14 {
		t.Fatalf("retention clamp status=%d window=%d", rec.Code, retained.WindowDays)
	}

	srv.Config().Usage.Enabled = false
	rec, disabled := getUsage(t, srv, "/api/apps/usage-guard/usage", ownerToken)
	if rec.Code != http.StatusOK || disabled.Enabled || disabled.Summary.Sessions != 0 {
		t.Fatalf("disabled response status=%d body=%+v", rec.Code, disabled)
	}
}

func TestAppUsagePseudonymousCountsWithoutExposingViewerKeys(t *testing.T) {
	srv, store := newTestServer(t)
	srv.Config().Usage = config.UsageConfig{Enabled: true, IdentityMode: config.UsageIdentityPseudonymous, RawRetentionDays: 30, AggregateRetentionDays: 365}
	adminToken, adminID := seedUserAndJWT(t, store, "usage-pseudo-admin", "admin")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "usage-pseudo", Name: "Pseudo", OwnerID: adminID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginUsageSession(db.UsageSessionStart{
		ID: "pseudo-session", Slug: "usage-pseudo", ViewerKey: "secret-app-scoped-key",
		PrincipalKind: "person", IdentityMode: "pseudonymous", InstanceID: "cp",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	rec, body := getUsage(t, srv, "/api/apps/usage-pseudo/usage?days=7", adminToken)
	if rec.Code != http.StatusOK || body.Summary.UniqueViewers == nil || *body.Summary.UniqueViewers != 1 {
		t.Fatalf("pseudonymous response status=%d body=%+v", rec.Code, body)
	}
	if body.IdentityDetail || len(body.Viewers) != 0 || len(body.Recent) != 0 {
		t.Fatal("pseudonymous mode exposed identity detail")
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("secret-app-scoped-key")) {
		t.Fatal("pseudonym escaped into API response")
	}
}
