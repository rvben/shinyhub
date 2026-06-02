package auth_test

import (
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/rvben/shinyhub/internal/auth"
)

const testSecret = "test-secret-at-least-32-characters-long"

func TestHashPassword_UsesStrongCost(t *testing.T) {
	h, err := auth.HashPassword("a-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	cost, err := bcrypt.Cost([]byte(h))
	if err != nil {
		t.Fatalf("bcrypt.Cost: %v", err)
	}
	if cost < 12 {
		t.Errorf("bcrypt cost = %d, want >= 12 (OWASP minimum for current hardware)", cost)
	}
	// The hash must still verify.
	if err := auth.VerifyPassword(h, "a-password"); err != nil {
		t.Errorf("VerifyPassword on fresh hash: %v", err)
	}
}

func TestIssueJWT_SetsNotBefore(t *testing.T) {
	tok, err := auth.IssueJWT(1, "alice", "admin", testSecret)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	claims, err := auth.ValidateJWT(tok, testSecret, nil)
	if err != nil {
		t.Fatalf("ValidateJWT: %v", err)
	}
	if claims.NotBefore == nil {
		t.Fatal("JWT must set a NotBefore (nbf) claim")
	}
	if claims.NotBefore.After(time.Now().Add(time.Minute)) {
		t.Errorf("nbf is in the future: %v", claims.NotBefore)
	}
}
