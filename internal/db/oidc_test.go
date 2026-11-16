package db_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestOIDCLinkPreservesForwardUserAndRejectsConflicts(t *testing.T) {
	s := dbtest.New(t)
	original, err := s.CreateForwardAuthUser("abc12345", "developer")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LinkOIDCUser(original.Username, "subject-a", true, original.ID+1); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("changed target identity accepted: %v", err)
	}
	preview, linked, err := s.LinkOIDCUser(original.Username, "subject-a", true)
	if err != nil || linked || preview.ID != original.ID {
		t.Fatalf("preview=%v linked=%v err=%v", preview, linked, err)
	}
	if _, err := s.GetUserByOAuthAccount("oidc", "subject-a"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("preview created link: %v", err)
	}
	u, linked, err := s.LinkOIDCUser(original.Username, "subject-a", false)
	if err != nil || !linked || u.ID != original.ID {
		t.Fatalf("link=%v %v %v", u, linked, err)
	}
	u, linked, err = s.LinkOIDCUser(original.Username, "subject-a", false)
	if err != nil || linked || u.ID != original.ID {
		t.Fatalf("idempotency=%v %v %v", u, linked, err)
	}
	if _, _, err = s.LinkOIDCUser(original.Username, "subject-b", false); !errors.Is(err, db.ErrOIDCLinkConflict) {
		t.Fatalf("second identity accepted: %v", err)
	}
	other, err := s.CreateForwardAuthUser("other", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.LinkOIDCUser(other.Username, "subject-a", false); !errors.Is(err, db.ErrOIDCLinkConflict) {
		t.Fatalf("identity takeover accepted: %v", err)
	}
	if err := s.CreateUser(db.CreateUserParams{Username: "local", PasswordHash: "$2a$local", Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.LinkOIDCUser("local", "local-sub", false); !errors.Is(err, db.ErrOIDCLinkConflict) {
		t.Fatalf("password user accepted: %v", err)
	}
	system, err := s.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.LinkOIDCUser(system.Username, "system-sub", false); !errors.Is(err, db.ErrOIDCLinkConflict) {
		t.Fatalf("system user accepted: %v", err)
	}
}
func TestOIDCAuthorityBinding(t *testing.T) {
	s := dbtest.New(t)
	for _, c := range []string{"client", "client"} {
		if err := s.BindOIDCProvider("https://issuer", c); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.BindOIDCProvider("https://attacker", "client"); err == nil {
		t.Fatal("issuer replacement accepted")
	}
	if err := s.BindOIDCProvider("https://issuer", "other-client"); err == nil {
		t.Fatal("client replacement accepted")
	}
}
func TestOIDCConcurrentLinkConverges(t *testing.T) {
	s := dbtest.New(t)
	u, err := s.CreateForwardAuthUser("same", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			v, _, e := s.LinkOIDCUser(u.Username, "same-sub", false)
			if e != nil || v.ID != u.ID {
				t.Errorf("concurrent link: %v %v", v, e)
			}
		})
	}
	wg.Wait()
}
func TestOIDCFrontchannelRevokesOnlyMatchingSessions(t *testing.T) {
	s := dbtest.New(t)
	u, err := s.CreateForwardAuthUser("person", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []db.OIDCSession{{JTI: "a", SID: "sid"}, {JTI: "b", SID: "sid"}, {JTI: "c", SID: "other"}} {
		v.Issuer = "https://issuer"
		v.UserID = u.ID
		v.IDToken = "encrypted"
		v.ExpiresAt = time.Now().Add(time.Hour)
		if err := s.SaveOIDCSession(v); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RevokeOIDCSessions("https://issuer", "sid"); err != nil {
		t.Fatal(err)
	}
	// Exercise the normal revocation cleanup using its actual unix encoding.
	if err := s.RevokeToken("cleanup-trigger", u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		revoked, err := s.IsTokenRevoked(id)
		if err != nil || revoked != (id != "c") {
			t.Errorf("revoked %s=%v %v", id, revoked, err)
		}
	}
}

func TestOIDCEmptyBindingRecoveryRefusesEstablishedIdentities(t *testing.T) {
	s := dbtest.New(t)
	if err := s.BindOIDCProvider("https://wrong", "wrong"); err != nil {
		t.Fatal(err)
	}
	if err := s.RebindEmptyOIDCProvider("https://correct", "correct"); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckOIDCProvider("https://correct", "correct"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateForwardAuthUser("linked", "viewer"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LinkOIDCUser("linked", "sub", false); err != nil {
		t.Fatal(err)
	}
	if err := s.RebindEmptyOIDCProvider("https://third", "third"); err == nil {
		t.Fatal("rebound established identity")
	}
	if err := s.CheckOIDCProvider("https://correct", "correct"); err != nil {
		t.Fatal("failed recovery changed binding")
	}
}

func TestOIDCEmptyRebindRefusesLiveSessionsAndEpochClearsHints(t *testing.T) {
	s := dbtest.New(t)
	if err := s.BindOIDCProvider("https://old", "old"); err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateForwardAuthUser("proxy", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOIDCSession(db.OIDCSession{JTI: "pending", UserID: u.ID, Issuer: "https://old", SID: "sid", IDToken: "ciphertext", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.RebindEmptyOIDCProvider("https://new", "new"); err == nil {
		t.Fatal("rebound live session")
	}
	if err := s.BumpTokenEpoch(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetOIDCSession("pending"); !errors.Is(err, db.ErrNotFound) {
		t.Fatal("revoked user's hint retained")
	}
	if err := s.RebindEmptyOIDCProvider("https://new", "new"); err != nil {
		t.Fatal(err)
	}
}

func TestOIDCSessionRenewalAndRevocationRetainExtendedDeadline(t *testing.T) {
	s := dbtest.New(t)
	u, err := s.CreateForwardAuthUser("extended", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	if err := s.SaveOIDCSession(db.OIDCSession{JTI: "renewed", UserID: u.ID, Issuer: "https://issuer", SID: "sid", IDToken: "hint", ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := s.ExtendOIDCSession("renewed", deadline); err != nil {
		t.Fatal(err)
	}
	v, err := s.GetOIDCSession("renewed")
	if err != nil || !v.ExpiresAt.Equal(deadline) {
		t.Fatalf("correlation deadline lost: %v %v", v, err)
	}
	retain := deadline.Add(time.Hour)
	if err := s.RevokeOIDCSessions("https://issuer", "sid", retain); err != nil {
		t.Fatal(err)
	}
	var expires int64
	if err := s.DB().QueryRow(`SELECT expires_at FROM revoked_tokens WHERE jti = ?`, "renewed").Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if expires != retain.Unix() {
		t.Fatalf("short retention: %d", expires)
	}
}
