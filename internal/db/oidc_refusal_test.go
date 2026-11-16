package db_test

import (
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestOIDCRefusalRevocationRequiresExactBoundIdentity(t *testing.T) {
	s := dbtest.New(t)
	u, err := s.CreateForwardAuthUser("linked", "developer")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LinkOIDCUser(u.Username, "sub", false); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeOIDCUserSessions("https://issuer", "client", "sub"); err != nil {
		t.Fatal(err)
	}
	if err := s.BindOIDCProvider("https://issuer", "client"); err != nil {
		t.Fatal(err)
	}
	for _, authority := range [][3]string{{"https://wrong", "client", "sub"}, {"https://issuer", "wrong", "sub"}, {"https://issuer", "client", "unknown"}} {
		if err := s.RevokeOIDCUserSessions(authority[0], authority[1], authority[2]); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.GetUserByID(u.ID)
	if err != nil || before.TokenEpoch != u.TokenEpoch {
		t.Fatalf("wrong identity changed: %+v %v", before, err)
	}
	if err := s.SaveOIDCSession(db.OIDCSession{JTI: "refused-hint", UserID: u.ID, Issuer: "https://issuer", SID: "sid", IDToken: "encrypted", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeOIDCUserSessions("https://issuer", "client", "sub"); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetUserByID(u.ID)
	if err != nil || after.TokenEpoch != before.TokenEpoch+1 || after.Role != before.Role {
		t.Fatalf("linked revocation: %+v %v", after, err)
	}
	if _, err := s.GetOIDCSession("refused-hint"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("hint retained: %v", err)
	}
}
