package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestOAuthStateReturnPathIsProviderBoundAndSingleUse(t *testing.T) {
	s := dbtest.New(t)
	const path = "/app/demo/?_inputs_&q=a%20b#plot"
	if err := s.CreateOAuthStateWithReturnPath("nonce", "oidc", path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeOAuthStateWithReturnPath("nonce", "github"); err == nil {
		t.Fatal("wrong provider consumed destination")
	}
	if got, err := s.ConsumeOAuthStateWithReturnPath("nonce", "oidc"); err != nil || got != path {
		t.Fatalf("destination=%q err=%v", got, err)
	}
	if _, err := s.ConsumeOAuthStateWithReturnPath("nonce", "oidc"); err == nil {
		t.Fatal("nonce was reusable")
	}
	// States created by the old API continue to work with an empty destination.
	if err := s.CreateOAuthState("legacy", "google"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ConsumeOAuthStateWithReturnPath("legacy", "google"); err != nil || got != "" {
		t.Fatalf("legacy destination=%q err=%v", got, err)
	}
	if err := s.CreateOAuthStateWithReturnPath("expired", "oidc", path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE oauth_states SET created_at = '2000-01-01 00:00:00' WHERE state = 'expired'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeOAuthStateWithReturnPath("expired", "oidc"); err == nil {
		t.Fatal("expired nonce returned a destination")
	}
}
