package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
)

// TestSessionEpoch_MismatchRejected pins the revocation mechanism: a JWT
// minted at epoch N stops authenticating once the user's live token epoch
// moves past N (admin revoke-sessions or a password change), even though the
// signature and expiry are still valid.
func TestSessionEpoch_MismatchRejected(t *testing.T) {
	secret := "test-secret"
	tok, err := auth.IssueJWT(7, "bob", "developer", secret) // epoch 0
	if err != nil {
		t.Fatal(err)
	}

	lookupWithEpoch := func(epoch int64) auth.UserLookup {
		return func(userID int64) (*auth.ContextUser, error) {
			return &auth.ContextUser{ID: userID, Username: "bob", Role: "developer", TokenEpoch: epoch}, nil
		}
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	if u, _, err := auth.AuthenticateRequest(req, secret, nil, lookupWithEpoch(0), nil); err != nil || u == nil {
		t.Fatalf("epoch-0 token with epoch-0 user must authenticate, got %v", err)
	}
	if _, _, err := auth.AuthenticateRequest(req, secret, nil, lookupWithEpoch(1), nil); err == nil {
		t.Fatal("epoch-0 token must be rejected once the user's epoch is 1 (sessions revoked)")
	}
}

// TestIssueSessionToken_CarriesEpoch pins that production session issuance
// embeds the user's current epoch, so a fresh login after a revocation works.
func TestIssueSessionToken_CarriesEpoch(t *testing.T) {
	secret := "test-secret"
	u := &auth.ContextUser{ID: 7, Username: "bob", Role: "developer", TokenEpoch: 3}
	tok, err := auth.IssueSessionToken(u, secret)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ValidateJWT(tok, secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	if claims.SessionEpoch != 3 {
		t.Errorf("sess_epoch = %d, want 3", claims.SessionEpoch)
	}
	if claims.AuthTime == nil || time.Since(claims.AuthTime.Time) > time.Minute {
		t.Errorf("fresh session token must stamp auth_time to now, got %v", claims.AuthTime)
	}

	slid, err := auth.SlideSessionToken(u, secret, time.Now().Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	sc, err := auth.ValidateJWT(slid, secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sc.SessionEpoch != 3 {
		t.Errorf("slid sess_epoch = %d, want 3", sc.SessionEpoch)
	}
	if sc.AuthTime == nil || time.Since(sc.AuthTime.Time) < 90*time.Minute {
		t.Errorf("sliding must preserve the original auth_time, got %v", sc.AuthTime)
	}
}

// mkEpochRequest is a tiny helper kept for symmetry with other auth tests.
func mkEpochRequest(tok string) *http.Request {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	return req
}
