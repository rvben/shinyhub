package db_test

import (
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func newMigratedStore(t *testing.T, dsn string) *db.Store {
	t.Helper()
	if dsn == ":memory:" {
		return dbtest.New(t)
	}
	// Non-memory DSN (e.g. file path for concurrency tests): open directly.
	store, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

// A first login provisions exactly one linked user and reports created=true;
// a subsequent login for the same identity returns that same user without
// creating another and reports created=false.
func TestProvisionOAuthUser_Idempotent(t *testing.T) {
	store := newMigratedStore(t, ":memory:")

	p := db.ProvisionOAuthUserParams{
		Provider:           "github",
		ProviderID:         "12345",
		UsernameCandidates: []string{"octocat", "octocat-gh12345"},
		Role:               "developer",
	}

	u1, created1, err := store.ProvisionOAuthUser(p)
	if err != nil {
		t.Fatalf("first provision: %v", err)
	}
	if !created1 {
		t.Fatal("first provision: created=false, want true")
	}
	if u1.Username != "octocat" || u1.Role != "developer" {
		t.Fatalf("first provision: got %q/%q", u1.Username, u1.Role)
	}

	linked, err := store.GetUserByOAuthAccount("github", "12345")
	if err != nil {
		t.Fatalf("get linked: %v", err)
	}
	if linked.ID != u1.ID {
		t.Fatalf("linked user %d != provisioned %d", linked.ID, u1.ID)
	}

	u2, created2, err := store.ProvisionOAuthUser(p)
	if err != nil {
		t.Fatalf("second provision: %v", err)
	}
	if created2 {
		t.Fatal("second provision: created=true, want false (must not create a duplicate)")
	}
	if u2.ID != u1.ID {
		t.Fatalf("second provision returned a different user: %d != %d", u2.ID, u1.ID)
	}
}

// When the preferred username is already taken by an unrelated account, the
// next candidate is used instead of failing.
func TestProvisionOAuthUser_UsernameCollisionFallback(t *testing.T) {
	store := newMigratedStore(t, ":memory:")

	if err := store.CreateUser(db.CreateUserParams{Username: "octocat", PasswordHash: "x", Role: "viewer"}); err != nil {
		t.Fatalf("seed conflicting user: %v", err)
	}

	u, created, err := store.ProvisionOAuthUser(db.ProvisionOAuthUserParams{
		Provider:           "github",
		ProviderID:         "999",
		UsernameCandidates: []string{"octocat", "octocat-gh999"},
		Role:               "developer",
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if !created || u.Username != "octocat-gh999" {
		t.Fatalf("got created=%v username=%q, want true/octocat-gh999", created, u.Username)
	}
}

// Exhausting every candidate username is a hard error, not a silent
// unlinked-user / no-link state.
func TestProvisionOAuthUser_AllCandidatesTaken(t *testing.T) {
	store := newMigratedStore(t, ":memory:")
	for _, n := range []string{"a", "b"} {
		if err := store.CreateUser(db.CreateUserParams{Username: n, PasswordHash: "x", Role: "viewer"}); err != nil {
			t.Fatalf("seed %q: %v", n, err)
		}
	}
	if _, _, err := store.ProvisionOAuthUser(db.ProvisionOAuthUserParams{
		Provider:           "oidc",
		ProviderID:         "sub-1",
		UsernameCandidates: []string{"a", "b"},
		Role:               "developer",
	}); err == nil {
		t.Fatal("expected error when all candidate usernames are taken, got nil")
	}
}

// Concurrent first logins for the same external identity must converge on a
// single user: every caller gets the same user ID, exactly one reports
// created=true, and no orphan (unlinked) user rows are left behind.
func TestProvisionOAuthUser_ConcurrentFirstLoginConverges(t *testing.T) {
	dbtest.SkipIfPostgres(t) // uses a SQLite file DSN for concurrent WAL writes
	store := newMigratedStore(t, t.TempDir()+"/race.db")

	const n = 16
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		ids     []int64
		createN int
		errN    int
	)
	for range n {
		wg.Go(func() {
			u, created, err := store.ProvisionOAuthUser(db.ProvisionOAuthUserParams{
				Provider:           "github",
				ProviderID:         "race-1",
				UsernameCandidates: []string{"racer", "racer-alt", "racer-alt2"},
				Role:               "developer",
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errN++
				return
			}
			ids = append(ids, u.ID)
			if created {
				createN++
			}
		})
	}
	wg.Wait()

	if errN != 0 {
		t.Fatalf("%d/%d concurrent provisions errored", errN, n)
	}
	if len(ids) != n {
		t.Fatalf("got %d results, want %d", len(ids), n)
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("callers disagreed on user: %v", ids)
		}
	}
	if createN != 1 {
		t.Fatalf("created=true reported %d times, want exactly 1", createN)
	}

	// Exactly one user row total: losers must have rolled back the user
	// they speculatively created, leaving no orphans.
	var userCount int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&userCount); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if userCount != 1 {
		t.Fatalf("user rows = %d, want 1 (orphans leaked)", userCount)
	}
	var linkCount int
	if err := store.DB().QueryRow(
		`SELECT COUNT(*) FROM oauth_accounts WHERE provider='github' AND provider_id='race-1'`).Scan(&linkCount); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if linkCount != 1 {
		t.Fatalf("oauth_account rows = %d, want 1", linkCount)
	}
}
