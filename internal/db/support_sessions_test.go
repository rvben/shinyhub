package db_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestSupportSessionLifecycleRevokesItsBoundToken(t *testing.T) {
	store := dbtest.New(t)
	for _, user := range []db.CreateUserParams{
		{Username: "admin", PasswordHash: "hash", Role: "admin"},
		{Username: "alice", PasswordHash: "hash", Role: "developer"},
	} {
		if err := store.CreateUser(user); err != nil {
			t.Fatal(err)
		}
	}
	admin, _ := store.GetUserByUsername("admin")
	alice, _ := store.GetUserByUsername("alice")
	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "sales", Name: "Sales", ProjectSlug: "default", OwnerID: admin.ID, Access: "public",
	}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("sales")
	expires := time.Now().UTC().Add(db.SupportSessionDuration)
	if err := store.CreateSupportSession(db.CreateSupportSessionParams{
		ID: "support-id", ActorUserID: admin.ID, ActorUsername: admin.Username,
		ActorTokenEpoch: admin.TokenEpoch, SubjectUserID: alice.ID, SubjectUsername: alice.Username,
		SubjectRole: alice.Role, SubjectTokenEpoch: alice.TokenEpoch, AppID: app.ID, AppSlug: "sales",
		Reason: "Investigating SUP-1042", LaunchCodeHash: "launch-hash", ExpiresAt: expires,
	}); err != nil {
		t.Fatal(err)
	}
	user, err := store.ConsumeAppLaunchCode("launch-hash", "sales")
	if err != nil || user.SupportSession == nil {
		t.Fatalf("consume support launch: user=%+v err=%v", user, err)
	}
	if _, err := store.ConsumeAppLaunchCode("launch-hash", "sales"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("replay error = %v, want not found", err)
	}
	if err := store.ActivateSupportSession("support-id", "support-jti", expires); err != nil {
		t.Fatal(err)
	}
	stopped, err := store.StopSupportSession("support-id", "ended_by_actor", "192.0.2.1")
	if err != nil || stopped.StoppedAt == nil || stopped.StopReason != "ended_by_actor" {
		t.Fatalf("stop result = %+v, err=%v", stopped, err)
	}
	if !stopped.NewlyStopped {
		t.Fatal("first stop must be identified as the audit winner")
	}
	var stopDetailJSON string
	if err := store.DB().QueryRow(`SELECT detail FROM audit_events WHERE action = 'support_session.stop' AND resource_id = ?`, "support-id").Scan(&stopDetailJSON); err != nil {
		t.Fatal(err)
	}
	var stopDetail map[string]any
	if err := json.Unmarshal([]byte(stopDetailJSON), &stopDetail); err != nil {
		t.Fatal(err)
	}
	if stopDetail["stop_reason"] != "ended_by_actor" || stopDetail["expires_at"] == nil {
		t.Fatalf("stop audit detail = %#v", stopDetail)
	}
	revoked, err := store.IsTokenRevoked("support-jti")
	if err != nil || !revoked {
		t.Fatalf("token revoked = %v, err=%v", revoked, err)
	}
	// Ending twice is safe for banner retries and does not lose the first cause.
	again, err := store.StopSupportSession("support-id", "retry", "192.0.2.1")
	if err != nil || again.StopReason != "ended_by_actor" {
		t.Fatalf("idempotent stop = %+v, err=%v", again, err)
	}
	if again.NewlyStopped {
		t.Fatal("idempotent stop must not produce a second audit winner")
	}
}

func TestSupportLaunchRejectsSubjectRoleExpansionAndAbortReleasesActor(t *testing.T) {
	store := dbtest.New(t)
	for _, user := range []db.CreateUserParams{
		{Username: "admin", PasswordHash: "hash", Role: "admin"},
		{Username: "alice", PasswordHash: "hash", Role: "viewer"},
	} {
		if err := store.CreateUser(user); err != nil {
			t.Fatal(err)
		}
	}
	admin, _ := store.GetUserByUsername("admin")
	alice, _ := store.GetUserByUsername("alice")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "sales", Name: "Sales", OwnerID: admin.ID, Access: "public"}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("sales")
	params := db.CreateSupportSessionParams{
		ID: "role-snapshot", ActorUserID: admin.ID, ActorUsername: admin.Username, ActorTokenEpoch: admin.TokenEpoch,
		SubjectUserID: alice.ID, SubjectUsername: alice.Username, SubjectRole: alice.Role, SubjectTokenEpoch: alice.TokenEpoch,
		AppID: app.ID, AppSlug: app.Slug, Reason: "Investigating SUP-3001", LaunchCodeHash: "role-hash",
		ExpiresAt: time.Now().Add(db.SupportSessionDuration),
	}
	if err := store.CreateSupportSession(params); err != nil {
		t.Fatal(err)
	}
	if err := store.SetManualRole(alice.ID, "developer"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeAppLaunchCode("role-hash", "sales"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("consume after role expansion = %v, want not found", err)
	}
	if err := store.AbortSupportSession("role-snapshot", "activation_failed"); err != nil {
		t.Fatal(err)
	}
	params.ID, params.LaunchCodeHash = "replacement", "replacement-hash"
	if err := store.CreateSupportSession(params); err != nil {
		t.Fatalf("aborted launch did not release actor: %v", err)
	}
}

func TestActivatedButUnobservedSupportSessionIsReapedAndRevoked(t *testing.T) {
	store := dbtest.New(t)
	for _, user := range []db.CreateUserParams{
		{Username: "admin", PasswordHash: "hash", Role: "admin"},
		{Username: "alice", PasswordHash: "hash", Role: "viewer"},
	} {
		if err := store.CreateUser(user); err != nil {
			t.Fatal(err)
		}
	}
	admin, _ := store.GetUserByUsername("admin")
	alice, _ := store.GetUserByUsername("alice")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "sales", Name: "Sales", OwnerID: admin.ID, Access: "public"}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("sales")
	params := db.CreateSupportSessionParams{
		ID: "unobserved", ActorUserID: admin.ID, ActorUsername: admin.Username, ActorTokenEpoch: admin.TokenEpoch,
		SubjectUserID: alice.ID, SubjectUsername: alice.Username, SubjectRole: alice.Role, SubjectTokenEpoch: alice.TokenEpoch,
		AppID: app.ID, AppSlug: app.Slug, Reason: "Investigating SUP-5001", LaunchCodeHash: "unobserved-hash",
		ExpiresAt: time.Now().Add(db.SupportSessionDuration),
	}
	if err := store.CreateSupportSession(params); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeAppLaunchCode("unobserved-hash", "sales"); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateSupportSession("unobserved", "orphan-jti", params.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE support_sessions SET created_at = datetime('now', '-2 minutes') WHERE id = ?`, "unobserved"); err != nil {
		t.Fatal(err)
	}
	params.ID, params.LaunchCodeHash = "replacement-after-orphan", "replacement-after-orphan-hash"
	if err := store.CreateSupportSession(params); err != nil {
		t.Fatalf("orphan activation was not reaped: %v", err)
	}
	if revoked, err := store.IsTokenRevoked("orphan-jti"); err != nil || !revoked {
		t.Fatalf("orphan token revoked=%v err=%v", revoked, err)
	}
	var orphanReason string
	if err := store.DB().QueryRow(`SELECT json_extract(detail, '$.stop_reason') FROM audit_events
		WHERE action = 'support_session.stop' AND resource_id = ?`, "unobserved").Scan(&orphanReason); err != nil {
		t.Fatal(err)
	}
	if orphanReason != "activation_abandoned" {
		t.Fatalf("orphan stop audit reason = %q", orphanReason)
	}
}

func TestSupportStartAuditFailureRollsBackSession(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: "hash", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_support_start_audit BEFORE INSERT ON audit_events
		WHEN NEW.action = 'support_session.start' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	admin, _ := store.GetUserByUsername("admin")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "sales", Name: "Sales", OwnerID: admin.ID, Access: "public"}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("sales")
	err := store.CreateSupportSession(db.CreateSupportSessionParams{
		ID: "rollback-start", ActorUserID: admin.ID, ActorUsername: admin.Username, ActorTokenEpoch: admin.TokenEpoch,
		SubjectUserID: admin.ID, SubjectUsername: "subject", SubjectRole: "viewer", SubjectTokenEpoch: 0,
		AppID: app.ID, AppSlug: "sales", Reason: "Investigating SUP-7001", LaunchCodeHash: "rollback-start-hash",
		ExpiresAt: time.Now().Add(db.SupportSessionDuration), AuditDetail: `{}`,
	})
	if err == nil {
		t.Fatal("start succeeded despite audit failure")
	}
	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM support_sessions WHERE id = ?`, "rollback-start").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled-back session count=%d err=%v", count, err)
	}
}

func TestSupportStopAuditFailureRollsBackStopAndRevocation(t *testing.T) {
	store := dbtest.New(t)
	for _, user := range []db.CreateUserParams{{Username: "admin", PasswordHash: "hash", Role: "admin"}, {Username: "alice", PasswordHash: "hash", Role: "viewer"}} {
		if err := store.CreateUser(user); err != nil {
			t.Fatal(err)
		}
	}
	admin, _ := store.GetUserByUsername("admin")
	alice, _ := store.GetUserByUsername("alice")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "sales", Name: "Sales", OwnerID: admin.ID, Access: "public"}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("sales")
	expires := time.Now().Add(db.SupportSessionDuration)
	if err := store.CreateSupportSession(db.CreateSupportSessionParams{
		ID: "rollback-stop", ActorUserID: admin.ID, ActorUsername: admin.Username, ActorTokenEpoch: admin.TokenEpoch,
		SubjectUserID: alice.ID, SubjectUsername: alice.Username, SubjectRole: alice.Role, SubjectTokenEpoch: alice.TokenEpoch,
		AppID: app.ID, AppSlug: app.Slug, Reason: "Investigating SUP-7002", LaunchCodeHash: "rollback-stop-hash", ExpiresAt: expires,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeAppLaunchCode("rollback-stop-hash", "sales"); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateSupportSession("rollback-stop", "rollback-jti", expires); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_support_stop_audit BEFORE INSERT ON audit_events
		WHEN NEW.action = 'support_session.stop' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StopSupportSession("rollback-stop", "ended_by_actor", "192.0.2.1"); err == nil {
		t.Fatal("stop succeeded despite audit failure")
	}
	var stopped any
	if err := store.DB().QueryRow(`SELECT stopped_at FROM support_sessions WHERE id = ?`, "rollback-stop").Scan(&stopped); err != nil || stopped != nil {
		t.Fatalf("stopped_at=%v err=%v, want active row", stopped, err)
	}
	if revoked, err := store.IsTokenRevoked("rollback-jti"); err != nil || revoked {
		t.Fatalf("revoked=%v err=%v, want rollback", revoked, err)
	}
}

func TestSupportLaunchFailsClosedAfterActorDemotion(t *testing.T) {
	store := dbtest.New(t)
	for _, user := range []db.CreateUserParams{
		{Username: "break-glass", PasswordHash: "hash", Role: "admin"},
		{Username: "admin", PasswordHash: "hash", Role: "admin"},
		{Username: "alice", PasswordHash: "hash", Role: "viewer"},
	} {
		if err := store.CreateUser(user); err != nil {
			t.Fatal(err)
		}
	}
	admin, _ := store.GetUserByUsername("admin")
	alice, _ := store.GetUserByUsername("alice")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "sales", Name: "Sales", ProjectSlug: "default", OwnerID: admin.ID, Access: "public"}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("sales")
	if err := store.CreateSupportSession(db.CreateSupportSessionParams{
		ID: "support-demote", ActorUserID: admin.ID, ActorUsername: admin.Username,
		ActorTokenEpoch: admin.TokenEpoch, SubjectUserID: alice.ID, SubjectUsername: alice.Username,
		SubjectRole: alice.Role, SubjectTokenEpoch: alice.TokenEpoch, AppID: app.ID, AppSlug: "sales",
		Reason: "Investigating SUP-1043", LaunchCodeHash: "demoted-launch", ExpiresAt: time.Now().Add(15 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetManualRole(admin.ID, "developer"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeAppLaunchCode("demoted-launch", "sales"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("consume after actor demotion = %v, want not found", err)
	}
}

func TestSupportLaunchRejectsRevokedCreationEpochAndConcurrentSession(t *testing.T) {
	store := dbtest.New(t)
	for _, user := range []db.CreateUserParams{
		{Username: "admin", PasswordHash: "hash", Role: "admin"},
		{Username: "alice", PasswordHash: "hash", Role: "viewer"},
	} {
		if err := store.CreateUser(user); err != nil {
			t.Fatal(err)
		}
	}
	admin, _ := store.GetUserByUsername("admin")
	alice, _ := store.GetUserByUsername("alice")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "sales", Name: "Sales", OwnerID: admin.ID, Access: "public"}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("sales")
	params := db.CreateSupportSessionParams{
		ID: "first", ActorUserID: admin.ID, ActorUsername: admin.Username, ActorTokenEpoch: admin.TokenEpoch,
		SubjectUserID: alice.ID, SubjectUsername: alice.Username, SubjectRole: alice.Role, SubjectTokenEpoch: alice.TokenEpoch,
		AppID: app.ID, AppSlug: app.Slug, Reason: "Investigating SUP-2001", LaunchCodeHash: "first-hash",
		ExpiresAt: time.Now().Add(db.SupportSessionDuration),
	}
	if err := store.CreateSupportSession(params); err != nil {
		t.Fatal(err)
	}
	second := params
	second.ID, second.LaunchCodeHash = "second", "second-hash"
	if err := store.CreateSupportSession(second); !errors.Is(err, db.ErrSupportSessionActive) {
		t.Fatalf("second active session error = %v, want active conflict", err)
	}
	if err := store.BumpTokenEpoch(admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeAppLaunchCode("first-hash", "sales"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("revoked creator launch error = %v, want not found", err)
	}
}
