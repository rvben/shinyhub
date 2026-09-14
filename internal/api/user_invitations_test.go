package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func inviteRequest(t *testing.T, srv *api.Server, action, token, password string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"token": token, "password": password})
	req := httptest.NewRequest("POST", "http://example.com/api/auth/invitations/"+action, bytes.NewReader(body))
	req.Header.Set("Origin", "http://example.com")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}
func createInvite(t *testing.T, srv *api.Server, admin, name, role string) (string, string) {
	t.Helper()
	body := []byte(fmt.Sprintf(`{"username":%q,"role":%q}`, name, role))
	req := authedRequest(t, "POST", "/api/user-invitations", body, admin)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Token      string            `json:"token"`
		Invitation db.UserInvitation `json:"invitation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Token, result.Invitation.ID
}
func TestUserInvitationLifecycle(t *testing.T) {
	srv, store := newTestServer(t)
	admin, _ := seedUserAndJWT(t, store, "admin", "admin")
	token, id := createInvite(t, srv, admin, "new-person", "operator")
	var storedHash string
	if err := store.DB().QueryRow(`SELECT token_hash FROM user_invitations WHERE id = ?`, id).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash == token || len(storedHash) != 64 {
		t.Fatal("invitation secret must only be stored as a digest")
	}
	if _, err := store.GetUserByUsername("new-person"); err != db.ErrNotFound {
		t.Fatal("invitation must not create an account")
	}
	preview := inviteRequest(t, srv, "preview", token, "")
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), `"operator"`) {
		t.Fatal(preview.Body.String())
	}
	list := httptest.NewRecorder()
	srv.Router().ServeHTTP(list, authedRequest(t, "GET", "/api/user-invitations", nil, admin))
	if strings.Contains(list.Body.String(), token) || strings.Contains(list.Body.String(), "token_hash") {
		t.Fatal("list exposed credential")
	}
	weak := inviteRequest(t, srv, "accept", token, "short")
	if weak.Code != 400 {
		t.Fatalf("weak: %d", weak.Code)
	}
	good := inviteRequest(t, srv, "accept", token, "a long personal passphrase")
	if good.Code != 201 {
		t.Fatalf("accept: %d %s", good.Code, good.Body.String())
	}
	user, err := store.GetUserByUsername("new-person")
	if err != nil {
		t.Fatal(err)
	}
	if user.Role != "operator" || user.ManualRole != "operator" || auth.VerifyPassword(user.PasswordHash, "a long personal passphrase") != nil {
		t.Fatal("wrong credentials or role")
	}
	if replay := inviteRequest(t, srv, "accept", token, "a different passphrase"); replay.Code != 404 {
		t.Fatalf("replay: %d", replay.Code)
	}
	pending, err := store.ListUserInvitations()
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending %v: %v", pending, err)
	}
}
func TestUserInvitationRevocationExpiryAndIssuer(t *testing.T) {
	for _, scenario := range []string{"revoked", "expired", "demoted", "deleted"} {
		t.Run(scenario, func(t *testing.T) {
			srv, store := newTestServer(t)
			admin, adminID := seedUserAndJWT(t, store, "admin", "admin")
			seedUserAndJWT(t, store, "backup-admin", "admin")
			token, id := createInvite(t, srv, admin, "person", "admin")
			switch scenario {
			case "revoked":
				rec := httptest.NewRecorder()
				srv.Router().ServeHTTP(rec, authedRequest(t, "DELETE", "/api/user-invitations/"+id, nil, admin))
				if rec.Code != 204 {
					t.Fatal(rec.Body.String())
				}
			case "expired":
				if _, err := store.DB().Exec(`UPDATE user_invitations SET expires_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Hour), id); err != nil {
					t.Fatal(err)
				}
			case "demoted":
				if err := store.SetManualRole(adminID, "viewer"); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := store.DeleteUser(adminID); err != nil {
					t.Fatal(err)
				}
			}
			if rec := inviteRequest(t, srv, "accept", token, "a long personal passphrase"); rec.Code != 404 {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
func TestUserInvitationConcurrentAcceptance(t *testing.T) {
	srv, store := newTestServer(t)
	admin, _ := seedUserAndJWT(t, store, "admin", "admin")
	token, _ := createInvite(t, srv, admin, "person", "viewer")
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- inviteRequest(t, srv, "accept", token, "a long personal passphrase").Code
		}()
	}
	wg.Wait()
	close(codes)
	successes := 0
	for code := range codes {
		if code == 201 {
			successes++
		} else if code != 404 && code != 409 {
			t.Fatalf("unexpected concurrent result: %d", code)
		}
	}
	if successes != 1 {
		t.Fatalf("successes %d", successes)
	}
}
func TestUserInvitationPermissionsAndSSOOnly(t *testing.T) {
	srv, store := newTestServer(t)
	admin, _ := seedUserAndJWT(t, store, "admin", "admin")
	viewer, _ := seedUserAndJWT(t, store, "viewer", "viewer")
	for _, method := range []string{"GET", "POST", "DELETE"} {
		path := "/api/user-invitations"
		if method == "DELETE" {
			path += "/missing"
		}
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, authedRequest(t, method, path, []byte(`{"username":"x"}`), viewer))
		if rec.Code != 403 {
			t.Fatalf("%s: %d", method, rec.Code)
		}
	}
	token, _ := createInvite(t, srv, admin, "person", "viewer")
	req := httptest.NewRequest("POST", "http://example.com/api/auth/invitations/accept", strings.NewReader(fmt.Sprintf(`{"token":%q,"password":"a long personal passphrase"}`, token)))
	req.Header.Set("Origin", "https://untrusted.example")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("cross-origin %d", rec.Code)
	}
	local := false
	cfg := &config.Config{Auth: config.AuthConfig{Secret: "test-secret", LocalLogin: &local}, Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()}}
	sso := api.New(cfg, dbtest.New(t), nil, nil)
	if rec := inviteRequest(t, sso, "preview", token, ""); rec.Code != 409 {
		t.Fatalf("SSO-only preview %d", rec.Code)
	}
}

func TestUserInvitationCollisionDoesNotOverwriteAccount(t *testing.T) {
	srv, store := newTestServer(t)
	admin, _ := seedUserAndJWT(t, store, "admin", "admin")
	token, _ := createInvite(t, srv, admin, "person", "admin")
	seedUserAndJWT(t, store, "person", "viewer")
	rec := inviteRequest(t, srv, "accept", token, "a long personal passphrase")
	if rec.Code != 409 {
		t.Fatalf("collision %d: %s", rec.Code, rec.Body.String())
	}
	user, err := store.GetUserByUsername("person")
	if err != nil || user.Role != "viewer" {
		t.Fatal("existing user must not be changed")
	}
	pending, err := store.ListUserInvitations()
	if err != nil || len(pending) != 1 {
		t.Fatal("failed transaction consumed invitation")
	}
}

func TestServiceAccountCannotInvite(t *testing.T) {
	srv, store := newTestServer(t)
	user, err := store.UpsertSystemUser("__deploy__", "admin")
	if err != nil {
		t.Fatal(err)
	}
	token := "shk_invitation_test_service_key"
	if _, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{UserID: user.ID, KeyHash: auth.HashAPIKey(token), Name: "invitation-test", CredentialType: "service", CredentialRole: "admin", Unrestricted: true}); err != nil {
		t.Fatal(err)
	}
	req := authedRequest(t, "POST", "/api/user-invitations", []byte(`{"username":"person","role":"admin"}`), token)
	req.Header.Set("Authorization", "Token "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("service account invitation %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUserInvitationReplacement(t *testing.T) {
	srv, store := newTestServer(t)
	admin, _ := seedUserAndJWT(t, store, "admin", "admin")
	oldToken, oldID := createInvite(t, srv, admin, "replacement-person", "operator")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, "POST", "/api/user-invitations/"+oldID+"/replace", nil, admin))
	if rec.Code != 201 {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Token      string            `json:"token"`
		Invitation db.UserInvitation `json:"invitation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Token == oldToken || result.Invitation.ID == oldID || result.Invitation.Username != "replacement-person" || result.Invitation.Role != "operator" {
		t.Fatal("replacement must rotate credentials and preserve identity and role")
	}
	if got := inviteRequest(t, srv, "preview", oldToken, ""); got.Code != 404 {
		t.Fatal("old link still works")
	}
	repeat := httptest.NewRecorder()
	srv.Router().ServeHTTP(repeat, authedRequest(t, "POST", "/api/user-invitations/"+oldID+"/replace", nil, admin))
	if repeat.Code != 409 {
		t.Fatalf("stale replacement: %d", repeat.Code)
	}
	if got := inviteRequest(t, srv, "accept", result.Token, "a long personal passphrase"); got.Code != 201 {
		t.Fatal(got.Body.String())
	}
}
