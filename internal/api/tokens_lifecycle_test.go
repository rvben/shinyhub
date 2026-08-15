package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// TestCreateToken_WithExpiry pins the bounded-expiry policy: expires_in_days
// sets expires_at, absent uses 90 days, and out-of-range values are rejected.
func TestCreateToken_WithExpiry(t *testing.T) {
	srv, store := newTestServer(t)
	_, tok := mkUser(t, store, "dev", "developer")

	rec := do(t, srv, "POST", "/api/tokens", tok, []byte(`{"name":"ci","expires_in_days":30}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Token     string     `json:"token"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ExpiresAt == nil {
		t.Fatal("expires_at missing from create response")
	}
	want := time.Now().Add(30 * 24 * time.Hour)
	if d := resp.ExpiresAt.Sub(want); d < -time.Hour || d > time.Hour {
		t.Errorf("expires_at = %v, want ~%v", resp.ExpiresAt, want)
	}

	rec = do(t, srv, "POST", "/api/tokens", tok, []byte(`{"name":"default-expiry"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create forever = %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ExpiresAt == nil {
		t.Fatal("token without an explicit expiry must use the 90-day default")
	}
	want = time.Now().Add(90 * 24 * time.Hour)
	if d := resp.ExpiresAt.Sub(want); d < -time.Hour || d > time.Hour {
		t.Errorf("default expires_at = %v, want ~%v", resp.ExpiresAt, want)
	}

	for _, bad := range []string{`{"name":"x","expires_in_days":-1}`, `{"name":"x","expires_in_days":40000}`} {
		rec = do(t, srv, "POST", "/api/tokens", tok, []byte(bad))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("create %s = %d, want 400", bad, rec.Code)
		}
	}
}

// TestConnectCLI_WithBrowserApprovedHash proves the browser pairing contract:
// only the hash crosses the browser/API boundary, while the raw token created
// by the CLI becomes usable after the signed-in user approves it. The response
// must never echo either value back into browser-visible JSON.
func TestConnectCLI_WithBrowserApprovedHash(t *testing.T) {
	srv, store := newTestServer(t)
	uid, session := mkUser(t, store, "dev", "developer")
	raw := "shk_" + strings.Repeat("c", 64)
	hash := auth.HashAPIKey(raw)
	pending := do(t, srv, "GET", "/api/auth/cli-connect/status?token_hash="+hash, "", nil)
	if pending.Code != http.StatusOK || !strings.Contains(pending.Body.String(), `"status":"pending"`) {
		t.Fatalf("pre-approval status = %d %s", pending.Code, pending.Body.String())
	}

	rec := do(t, srv, "POST", "/api/tokens/connect", session,
		[]byte(fmt.Sprintf(`{"name":"cli-workstation-a1b2c3","token_hash":%q}`, hash)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("connect = %d; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), raw) || strings.Contains(rec.Body.String(), hash) {
		t.Fatalf("connect response leaked credential material: %s", rec.Body.String())
	}
	approved := do(t, srv, "GET", "/api/auth/cli-connect/status?token_hash="+hash, "", nil)
	if approved.Code != http.StatusOK || !strings.Contains(approved.Body.String(), `"status":"approved"`) {
		t.Fatalf("post-approval status = %d %s", approved.Code, approved.Body.String())
	}

	connected := doToken(t, srv, "GET", "/api/auth/me", raw, nil)
	if connected.Code != http.StatusOK {
		t.Fatalf("browser-approved CLI token did not authenticate: %d %s", connected.Code, connected.Body.String())
	}
	var me struct {
		Credential struct {
			Type       string     `json:"type"`
			ID         int64      `json:"id"`
			Name       string     `json:"name"`
			CreatedAt  *time.Time `json:"created_at"`
			LastUsedAt *time.Time `json:"last_used_at"`
			ExpiresAt  *time.Time `json:"expires_at"`
		} `json:"credential"`
	}
	if err := json.Unmarshal(connected.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me.Credential.Type != "api_key" || me.Credential.ID == 0 || me.Credential.Name != "cli-workstation-a1b2c3" ||
		me.Credential.CreatedAt == nil || me.Credential.ExpiresAt == nil {
		t.Fatalf("safe credential lifecycle missing from /api/auth/me: %+v", me.Credential)
	}
	if strings.Contains(connected.Body.String(), raw) || strings.Contains(connected.Body.String(), hash) {
		t.Fatalf("/api/auth/me leaked credential material: %s", connected.Body.String())
	}

	keys, err := store.ListAPIKeys(uid)
	if err != nil || len(keys) != 1 {
		t.Fatalf("saved keys = %+v, err=%v", keys, err)
	}
	if keys[0].ExpiresAt == nil {
		t.Fatal("browser-approved CLI token must expire")
	}
	want := time.Now().Add(90 * 24 * time.Hour)
	if d := keys[0].ExpiresAt.Sub(want); d < -time.Hour || d > time.Hour {
		t.Errorf("expiry = %v, want about %v", keys[0].ExpiresAt, want)
	}
}

func TestConnectCLI_RejectsMalformedHash(t *testing.T) {
	srv, store := newTestServer(t)
	_, session := mkUser(t, store, "dev", "developer")
	for _, hash := range []string{"short", strings.Repeat("z", 64)} {
		rec := do(t, srv, "POST", "/api/tokens/connect", session,
			[]byte(fmt.Sprintf(`{"name":"laptop","token_hash":%q}`, hash)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("hash %q = %d, want 400", hash, rec.Code)
		}
	}
}

// TestExpiredToken_Rejected pins enforcement: an expired key is a 401 on any
// authenticated endpoint, indistinguishable from an unknown key.
func TestExpiredToken_Rejected(t *testing.T) {
	srv, store := newTestServer(t)
	uid, _ := mkUser(t, store, "dev", "developer")
	raw := "shk_" + strings.Repeat("e", 64)
	past := time.Now().UTC().Add(-time.Minute)
	if _, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{
		UserID: uid, KeyHash: auth.HashAPIKey(raw), Name: "old", ExpiresAt: &past,
	}); err != nil {
		t.Fatal(err)
	}
	rec := doToken(t, srv, "GET", "/api/apps", raw, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expired token = %d, want 401", rec.Code)
	}
}

// TestTokenUse_StampsLastUsed pins the last-used trail: after authenticating
// with a token once, the owner's token list shows a recent last_used_at.
func TestTokenUse_StampsLastUsed(t *testing.T) {
	srv, store := newTestServer(t)
	uid, jwtTok := mkUser(t, store, "dev", "developer")
	raw := "shk_" + strings.Repeat("f", 64)
	if _, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{
		UserID: uid, KeyHash: auth.HashAPIKey(raw), Name: "ci",
	}); err != nil {
		t.Fatal(err)
	}
	if rec := doToken(t, srv, "GET", "/api/apps", raw, nil); rec.Code != http.StatusOK {
		t.Fatalf("token use = %d", rec.Code)
	}

	rec := do(t, srv, "GET", "/api/tokens", jwtTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	var list struct {
		Items []struct {
			Name       string     `json:"name"`
			LastUsedAt *time.Time `json:"last_used_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(list.Items))
	}
	if list.Items[0].LastUsedAt == nil || time.Since(*list.Items[0].LastUsedAt) > time.Minute {
		t.Errorf("last_used_at = %v, want a recent stamp", list.Items[0].LastUsedAt)
	}
}

// TestListTokens_AdminInventory pins the governance surface: ?all=1 is
// admin-only and carries the owning username per token so revocation does not
// depend on audit archaeology.
func TestListTokens_AdminInventory(t *testing.T) {
	srv, store := newTestServer(t)
	devID, devTok := mkUser(t, store, "dev", "developer")
	_, adminTok := mkUser(t, store, "boss", "admin")
	if _, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{UserID: devID, KeyHash: "hx", Name: "devkey"}); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, "GET", "/api/tokens?all=1", devTok, nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("non-admin all=1 = %d, want 403", rec.Code)
	}

	rec = do(t, srv, "GET", "/api/tokens?all=1", adminTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin all=1 = %d; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"devkey", "dev"} {
		if !strings.Contains(body, fmt.Sprintf("%q", want)) {
			t.Errorf("inventory should mention %q, got %s", want, body)
		}
	}
}
