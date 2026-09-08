package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

const testStampedCommit = "abc123def456"

// newCommitTestServer builds a server whose binary reports a build commit.
// A test binary carries no VCS stamping, so without this the commit is "" for
// everyone and both halves of the test below would pass on a broken handler.
func newCommitTestServer(t *testing.T) (*Server, *db.Store) {
	t.Helper()
	store := dbtest.New(t)
	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
	}
	prev := currentCommit
	currentCommit = func() string { return testStampedCommit }
	t.Cleanup(func() { currentCommit = prev })
	return New(cfg, store, nil, nil), store
}

func serverInfoCommit(t *testing.T, srv *Server, cookie *http.Cookie) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/server-info", nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode server-info: %v", err)
	}
	return body.Commit
}

// The build commit names the exact revision, which is what turns a list of
// security fixes into a list of this server's unpatched ones. An anonymous
// caller has no reason to hold it: the field's only consumer is the About
// dialog, which is behind the login.
func TestServerInfoWithholdsTheBuildCommitFromAnAnonymousCaller(t *testing.T) {
	srv, _ := newCommitTestServer(t)
	if got := serverInfoCommit(t, srv, nil); got != "" {
		t.Fatalf("anonymous caller got the build commit %q", got)
	}
}

// The negative control. Withholding it from everyone would silently empty the
// About dialog's build line rather than protect anything.
func TestServerInfoGivesTheBuildCommitToASignedInCaller(t *testing.T) {
	srv, store := newCommitTestServer(t)
	token := seedCommitTestUser(t, store)

	got := serverInfoCommit(t, srv, &http.Cookie{Name: auth.SessionCookieName, Value: token})
	if got != testStampedCommit {
		t.Fatalf("signed-in caller got commit %q, want %q", got, testStampedCommit)
	}
}

// A cookie that is not a session this server issued must read as anonymous, not
// as a caller whose credential merely failed to parse.
func TestServerInfoWithholdsTheBuildCommitFromAnInvalidSession(t *testing.T) {
	srv, _ := newCommitTestServer(t)
	for _, value := range []string{"not-a-jwt", "", "a.b.c"} {
		got := serverInfoCommit(t, srv, &http.Cookie{Name: auth.SessionCookieName, Value: value})
		if got != "" {
			t.Fatalf("cookie %q got the build commit %q", value, got)
		}
	}
}

func seedCommitTestUser(t *testing.T, store *db.Store) string {
	t.Helper()
	hash, err := auth.HashPassword("seed-password-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, err := store.GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	token, err := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	return token
}
