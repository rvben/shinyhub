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

// TestConnectCLI_HappyPath proves the device-authorization pairing contract:
// the CLI registers its hash before any browser page opens and gets back a
// user code that only a person copying it off their own terminal can enter;
// entering that code is what turns the registered hash into a usable API key.
// The register/approve responses must never echo the hash or raw token back
// into browser-visible JSON.
func TestConnectCLI_HappyPath(t *testing.T) {
	srv, store := newTestServer(t)
	uid, session := mkUser(t, store, "dev", "developer")
	raw := "shk_" + strings.Repeat("c", 64)
	hash := auth.HashAPIKey(raw)

	reg := do(t, srv, "POST", "/api/auth/cli-connect/register", "",
		[]byte(fmt.Sprintf(`{"name":"cli-workstation-a1b2c3","token_hash":%q}`, hash)))
	if reg.Code != http.StatusCreated {
		t.Fatalf("register = %d; body=%s", reg.Code, reg.Body.String())
	}
	if strings.Contains(reg.Body.String(), raw) || strings.Contains(reg.Body.String(), hash) {
		t.Fatalf("register response leaked credential material: %s", reg.Body.String())
	}
	var regResp struct {
		UserCode  string    `json:"user_code"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(reg.Body.Bytes(), &regResp); err != nil {
		t.Fatal(err)
	}
	if regResp.UserCode == "" {
		t.Fatal("register response must include a user_code")
	}

	pending := do(t, srv, "GET", "/api/auth/cli-connect/status?token_hash="+hash, "", nil)
	if pending.Code != http.StatusOK || !strings.Contains(pending.Body.String(), `"status":"pending"`) {
		t.Fatalf("pre-approval status = %d %s", pending.Code, pending.Body.String())
	}

	rec := do(t, srv, "POST", "/api/auth/cli-connect/approve", session,
		[]byte(fmt.Sprintf(`{"user_code":%q}`, regResp.UserCode)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("approve = %d; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), raw) || strings.Contains(rec.Body.String(), hash) {
		t.Fatalf("approve response leaked credential material: %s", rec.Body.String())
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
	srv, _ := newTestServer(t)
	for _, hash := range []string{"short", strings.Repeat("z", 64)} {
		rec := do(t, srv, "POST", "/api/auth/cli-connect/register", "",
			[]byte(fmt.Sprintf(`{"name":"laptop","token_hash":%q}`, hash)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("hash %q = %d, want 400", hash, rec.Code)
		}
	}
}

// TestConnectCLI_UnregisteredHashNeverApproves proves the core fix for the
// phishing hole this replaces: a hash nobody registered through the CLI's own
// register call can never be reported as approved, because there is no
// pending request and no API key for it to match. The old design trusted a
// hash carried entirely in a browser-controlled URL; this one does not trust
// the browser for the hash at all.
func TestConnectCLI_UnregisteredHashNeverApproves(t *testing.T) {
	srv, store := newTestServer(t)
	_, session := mkUser(t, store, "dev", "developer")
	hash := auth.HashAPIKey("shk_" + strings.Repeat("d", 64))

	// An attacker who knows nothing but a plausible-looking hash cannot get it
	// approved: there is no user_code to submit for it, so even a signed-in
	// victim has nothing phishable to click through.
	status := do(t, srv, "GET", "/api/auth/cli-connect/status?token_hash="+hash, "", nil)
	if status.Code != http.StatusOK || strings.Contains(status.Body.String(), `"status":"approved"`) {
		t.Fatalf("unregistered hash status = %d %s, must never read approved", status.Code, status.Body.String())
	}

	// Confirm approval genuinely requires a matching pending request: even a
	// signed-in user cannot approve anything without a real code to submit.
	rec := do(t, srv, "POST", "/api/auth/cli-connect/approve", session, []byte(`{"user_code":"AAAA-1111"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("approve with no matching pending request = %d, want 400", rec.Code)
	}
}

// TestConnectCLI_WrongCodeRejected proves a code that does not match the
// pending request fails, and that a wrong guess does not burn the real one.
func TestConnectCLI_WrongCodeRejected(t *testing.T) {
	srv, store := newTestServer(t)
	_, session := mkUser(t, store, "dev", "developer")
	hash := auth.HashAPIKey("shk_" + strings.Repeat("e", 64))

	reg := do(t, srv, "POST", "/api/auth/cli-connect/register", "",
		[]byte(fmt.Sprintf(`{"name":"cli-laptop-e1e2e3","token_hash":%q}`, hash)))
	var regResp struct {
		UserCode string `json:"user_code"`
	}
	if err := json.Unmarshal(reg.Body.Bytes(), &regResp); err != nil {
		t.Fatal(err)
	}

	wrong := do(t, srv, "POST", "/api/auth/cli-connect/approve", session, []byte(`{"user_code":"ZZZZ-9999"}`))
	if wrong.Code != http.StatusBadRequest {
		t.Fatalf("wrong code approve = %d, want 400", wrong.Code)
	}

	right := do(t, srv, "POST", "/api/auth/cli-connect/approve", session,
		[]byte(fmt.Sprintf(`{"user_code":%q}`, regResp.UserCode)))
	if right.Code != http.StatusCreated {
		t.Fatalf("correct code after a wrong guess = %d; body=%s", right.Code, right.Body.String())
	}
}

// TestConnectCLI_MalformedCodeRejected proves the approve endpoint validates
// shape before ever touching the database.
func TestConnectCLI_MalformedCodeRejected(t *testing.T) {
	srv, store := newTestServer(t)
	_, session := mkUser(t, store, "dev", "developer")
	for _, code := range []string{"", "short", "ABCDEFGH", "AB CD-1234"} {
		rec := do(t, srv, "POST", "/api/auth/cli-connect/approve", session, []byte(fmt.Sprintf(`{"user_code":%q}`, code)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("code %q = %d, want 400", code, rec.Code)
		}
	}
}

// TestConnectCLI_ExpiredRequestCannotBeApproved proves the same TTL the DB
// layer enforces is actually reached through the HTTP handlers: a request
// registered long enough ago can no longer be approved by anyone, typed code
// or not.
func TestConnectCLI_ExpiredRequestCannotBeApproved(t *testing.T) {
	srv, store := newTestServer(t)
	_, session := mkUser(t, store, "dev", "developer")
	hash := auth.HashAPIKey("shk_" + strings.Repeat("f", 64))

	reg := do(t, srv, "POST", "/api/auth/cli-connect/register", "",
		[]byte(fmt.Sprintf(`{"name":"cli-laptop-f1f2f3","token_hash":%q}`, hash)))
	var regResp struct {
		UserCode string `json:"user_code"`
	}
	if err := json.Unmarshal(reg.Body.Bytes(), &regResp); err != nil {
		t.Fatal(err)
	}

	if _, err := store.DB().Exec(
		`UPDATE cli_connect_requests SET created_at = ? WHERE token_hash = ?`,
		time.Now().UTC().Add(-(db.CLIConnectRequestTTL+time.Minute)), hash,
	); err != nil {
		t.Fatal(err)
	}

	rec := do(t, srv, "POST", "/api/auth/cli-connect/approve", session,
		[]byte(fmt.Sprintf(`{"user_code":%q}`, regResp.UserCode)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("approve of an expired request = %d, want 400", rec.Code)
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
