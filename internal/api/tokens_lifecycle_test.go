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

// TestCreateToken_WithExpiry pins the opt-in expiry: expires_in_days sets
// expires_at about that far out; 0/absent keeps the token non-expiring;
// negative or absurd values 400.
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

	rec = do(t, srv, "POST", "/api/tokens", tok, []byte(`{"name":"forever"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create forever = %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ExpiresAt != nil {
		t.Errorf("token without expiry should have null expires_at, got %v", resp.ExpiresAt)
	}

	for _, bad := range []string{`{"name":"x","expires_in_days":-1}`, `{"name":"x","expires_in_days":40000}`} {
		rec = do(t, srv, "POST", "/api/tokens", tok, []byte(bad))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("create %s = %d, want 400", bad, rec.Code)
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
