package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
)

// meServer is a stand-in ShinyHub that accepts any token and reports a fixed
// user, so a login test can exercise the whole save path without a real server.
func meServer(t *testing.T, username string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/me":
			_, _ = w.Write([]byte(`{"user":{"username":"` + username + `","role":"developer"}}`))
		case "/api/auth/logout":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// loginTo runs a token login against host and returns the decoded action
// envelope. Output goes to a buffer, so stdout stays clean under `go test`.
func loginTo(t *testing.T, host, token, name string) map[string]any {
	t.Helper()
	cmd := &cobra.Command{}
	var buf strings.Builder
	cmd.SetOut(&buf)
	cmd.SetErr(&strings.Builder{})
	if err := runLogin(cmd, &loginFlags{host: host, token: token, name: name}); err != nil {
		t.Fatalf("login to %s: %v", host, err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(buf.String()), &res); err != nil {
		t.Fatalf("login output is not an action envelope: %q (%v)", buf.String(), err)
	}
	return res
}

// Signing in to a second server must add it, not replace the first. The
// single-slot file this replaces silently discarded the other credential and
// said "Logged in" either way, so the report has to distinguish the cases.
func TestLogin_AddsAHostWithoutDisturbingOthers(t *testing.T) {
	isolatedCredentials(t)
	first := meServer(t, "alice")
	second := meServer(t, "bob")

	if got := loginTo(t, first.URL, "shk_first", "one")["status"]; got != "added" {
		t.Errorf("first login status = %v, want %q", got, "added")
	}
	res := loginTo(t, second.URL, "shk_second", "two")
	if res["status"] != "added" {
		t.Errorf("second login status = %v, want %q", res["status"], "added")
	}
	if res["switched_from"] != first.URL {
		t.Errorf("switched_from = %v, want %q so the move of the current host is visible",
			res["switched_from"], first.URL)
	}

	st, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if len(st.Hosts) != 2 {
		t.Fatalf("got %d saved hosts %v, want both kept", len(st.Hosts), st.sortedHosts())
	}
	if st.Hosts[first.URL].Token != "shk_first" {
		t.Errorf("first host's token = %q, want it untouched", st.Hosts[first.URL].Token)
	}
	if st.CurrentHost != second.URL {
		t.Errorf("current host = %q, want the server just logged in to", st.CurrentHost)
	}
}

// Re-authenticating with the server you are already on is the routine case and
// must be reported as such - not as a new server, and not with a switch note.
func TestLogin_RefreshingTheCurrentHostSaysRefreshed(t *testing.T) {
	isolatedCredentials(t)
	srv := meServer(t, "alice")

	loginTo(t, srv.URL, "shk_v1", "prod")
	res := loginTo(t, srv.URL, "shk_v2", "")

	if res["status"] != "refreshed" {
		t.Errorf("status = %v, want %q", res["status"], "refreshed")
	}
	if got, ok := res["switched_from"]; ok && got != "" {
		t.Errorf("switched_from = %v, want empty when the current host did not move", got)
	}
	// Re-running login without --name must keep the alias the user chose.
	if res["name"] != "prod" {
		t.Errorf("name = %v, want the existing alias preserved", res["name"])
	}
	st, _ := loadStore()
	if st.Hosts[srv.URL].Token != "shk_v2" {
		t.Errorf("token = %q, want the refreshed value", st.Hosts[srv.URL].Token)
	}
}

// Omitting --host re-authenticates with the current server. Requiring the URL
// every time only forces it to be retyped when there is nothing to choose
// between.
func TestLogin_WithoutHostTargetsTheCurrentServer(t *testing.T) {
	isolatedCredentials(t)
	srv := meServer(t, "alice")
	loginTo(t, srv.URL, "shk_v1", "prod")

	res := loginTo(t, "", "shk_v2", "")
	if res["host"] != srv.URL {
		t.Errorf("host = %v, want the current server %q", res["host"], srv.URL)
	}
	if res["status"] != "refreshed" {
		t.Errorf("status = %v, want %q", res["status"], "refreshed")
	}
}

// With nothing saved there is no current server to infer, so --host is required
// and the error has to say so rather than failing later on an empty URL.
func TestLogin_WithoutHostAndNothingSavedAsksForIt(t *testing.T) {
	isolatedCredentials(t)

	cmd := &cobra.Command{}
	cmd.SetOut(&strings.Builder{})
	err := runLogin(cmd, &loginFlags{token: "shk_x"})
	if err == nil {
		t.Fatal("expected a validation error when no host is given and none is saved")
	}
	if kind, code := classify(err); kind != KindValidation || code != 1 {
		t.Errorf("classify = (%q,%d), want (%q,1)", kind, code, KindValidation)
	}
	if hint := hintOf(err); !strings.Contains(hint, "--host") {
		t.Errorf("hint should name --host, got %q", hint)
	}
}

// A host without a scheme cannot be turned into a request URL, and guessing one
// would decide over which protocol the credential travels.
func TestLogin_RejectsHostWithoutAScheme(t *testing.T) {
	isolatedCredentials(t)

	cmd := &cobra.Command{}
	cmd.SetOut(&strings.Builder{})
	err := runLogin(cmd, &loginFlags{host: "shiny.example.com", token: "shk_x"})
	if err == nil {
		t.Fatal("expected a validation error for a schemeless --host")
	}
	if kind, _ := classify(err); kind != KindValidation {
		t.Errorf("kind = %q, want %q", kind, KindValidation)
	}
	if hint := hintOf(err); !strings.Contains(hint, "https://") {
		t.Errorf("hint should show a scheme, got %q", hint)
	}
}

func TestLogin_NameValidation(t *testing.T) {
	cases := []struct {
		name     string
		alias    string
		wantHint string
	}{
		{"whitespace", "my prod", "single word"},
		{"looks like a url", "https://prod.example.com", "://"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolatedCredentials(t)
			srv := meServer(t, "alice")

			cmd := &cobra.Command{}
			cmd.SetOut(&strings.Builder{})
			err := runLogin(cmd, &loginFlags{host: srv.URL, token: "shk_x", name: tc.alias})
			if err == nil {
				t.Fatalf("expected --name %q to be rejected", tc.alias)
			}
			if kind, _ := classify(err); kind != KindValidation {
				t.Errorf("kind = %q, want %q", kind, KindValidation)
			}
			if hint := hintOf(err); !strings.Contains(hint, tc.wantHint) {
				t.Errorf("hint = %q, want it to mention %q", hint, tc.wantHint)
			}
			// Nothing may be saved when the name is refused.
			st, _ := loadStore()
			if len(st.Hosts) != 0 {
				t.Errorf("a rejected login saved %d hosts, want 0", len(st.Hosts))
			}
		})
	}
}

// A name bound to a second server would make `shinyhub use <name>` target
// whichever was saved last - a silent retarget of every scripted command.
func TestLogin_NameAlreadyUsedByAnotherHostIsRefused(t *testing.T) {
	isolatedCredentials(t)
	first := meServer(t, "alice")
	second := meServer(t, "bob")
	loginTo(t, first.URL, "shk_first", "prod")

	cmd := &cobra.Command{}
	cmd.SetOut(&strings.Builder{})
	err := runLogin(cmd, &loginFlags{host: second.URL, token: "shk_second", name: "prod"})
	if err == nil {
		t.Fatal("expected a duplicate --name to be refused")
	}
	if !strings.Contains(err.Error(), first.URL) {
		t.Errorf("error should name the host that already holds the alias, got: %v", err)
	}

	// The alias must still point where it did, and the second server must not
	// have been saved by the refused login.
	st, _ := loadStore()
	if owner, _ := st.nameOwner("prod"); owner != first.URL {
		t.Errorf("alias owner = %q, want it unchanged at %q", owner, first.URL)
	}
	if _, saved := st.Hosts[second.URL]; saved {
		t.Error("a refused login must not save the host")
	}
}

// Renaming the server you are already signed in to is not a conflict with
// itself.
func TestLogin_HostMayKeepOrChangeItsOwnName(t *testing.T) {
	isolatedCredentials(t)
	srv := meServer(t, "alice")
	loginTo(t, srv.URL, "shk_1", "prod")

	res := loginTo(t, srv.URL, "shk_2", "prod")
	if res["name"] != "prod" {
		t.Errorf("name = %v, want %q", res["name"], "prod")
	}
	res = loginTo(t, srv.URL, "shk_3", "production")
	if res["name"] != "production" {
		t.Errorf("name = %v, want the new alias %q", res["name"], "production")
	}
	st, _ := loadStore()
	if _, taken := st.nameOwner("prod"); taken {
		t.Error("the old alias should be gone after a rename")
	}
}

// The verification round-trip already knows who the token belongs to, so the
// saved entry records it and `shinyhub hosts` can answer offline.
func TestLogin_RecordsTheUsernameFromTheServer(t *testing.T) {
	isolatedCredentials(t)
	srv := meServer(t, "carol")

	res := loginTo(t, srv.URL, "shk_x", "")
	if res["user"] != "carol" {
		t.Errorf("user = %v, want the username the server reported", res["user"])
	}
	st, _ := loadStore()
	if st.Hosts[srv.URL].User != "carol" {
		t.Errorf("saved user = %q, want %q", st.Hosts[srv.URL].User, "carol")
	}
}

// A token the server rejects must not be written to disk: saving it would
// replace a working credential with one already known to be invalid.
func TestLogin_RejectedTokenLeavesTheStoreUntouched(t *testing.T) {
	isolatedCredentials(t)
	good := meServer(t, "alice")
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(bad.Close)

	loginTo(t, good.URL, "shk_good", "good")

	cmd := &cobra.Command{}
	cmd.SetOut(&strings.Builder{})
	if err := runLogin(cmd, &loginFlags{host: bad.URL, token: "shk_bad"}); err == nil {
		t.Fatal("expected a rejected token to fail")
	}

	st, _ := loadStore()
	if _, saved := st.Hosts[bad.URL]; saved {
		t.Error("a rejected token must not be saved")
	}
	if st.CurrentHost != good.URL {
		t.Errorf("current host = %q, want it left at %q", st.CurrentHost, good.URL)
	}
}

// Signing out of one server must leave the others signed in, and must leave a
// usable current host behind rather than dropping into "no current host".
func TestLogout_RemovesOneHostAndPromotesAnother(t *testing.T) {
	isolatedCredentials(t)
	keep := meServer(t, "alice")
	drop := meServer(t, "bob")
	loginTo(t, keep.URL, "shk_keep", "keep")
	loginTo(t, drop.URL, "shk_drop", "drop")

	cmd := newLogoutCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&strings.Builder{})
	if err := runLogoutWith(cmd, &logoutFlags{}); err != nil {
		t.Fatalf("logout: %v", err)
	}

	var res struct {
		Status         string `json:"status"`
		Host           string `json:"host"`
		CurrentHost    string `json:"current_host"`
		RemainingHosts int    `json:"remaining_hosts"`
	}
	if err := json.Unmarshal([]byte(out.String()), &res); err != nil {
		t.Fatalf("logout output is not an action envelope: %q (%v)", out.String(), err)
	}
	if res.Host != drop.URL {
		t.Errorf("logged out of %q, want the current host %q", res.Host, drop.URL)
	}
	if res.CurrentHost != keep.URL || res.RemainingHosts != 1 {
		t.Errorf("got current=%q remaining=%d, want %q and 1", res.CurrentHost, res.RemainingHosts, keep.URL)
	}

	st, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if _, still := st.Hosts[drop.URL]; still {
		t.Error("the host logged out of is still saved")
	}
	if st.Hosts[keep.URL].Token != "shk_keep" {
		t.Error("the other credential must survive")
	}
	// The file must still exist: there is a credential left in it.
	if _, err := os.Stat(configPath()); err != nil {
		t.Errorf("credentials file should remain while a host is still saved: %v", err)
	}
}

// --host signs out of a specific server without first switching to it.
func TestLogout_HostFlagSignsOutOfANamedServer(t *testing.T) {
	isolatedCredentials(t)
	current := meServer(t, "alice")
	other := meServer(t, "bob")
	loginTo(t, other.URL, "shk_other", "other")
	loginTo(t, current.URL, "shk_current", "current")

	hostFlagOverride = "other"
	cmd := newLogoutCmd()
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	if err := runLogoutWith(cmd, &logoutFlags{}); err != nil {
		t.Fatalf("logout --host other: %v", err)
	}
	hostFlagOverride = ""

	st, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if _, still := st.Hosts[other.URL]; still {
		t.Error("--host named server was not signed out")
	}
	if st.CurrentHost != current.URL {
		t.Errorf("current host = %q, want it unchanged at %q", st.CurrentHost, current.URL)
	}
}

// --all signs out everywhere: every server is asked to revoke, and the file
// goes with them.
func TestLogout_AllRevokesEverywhereAndRemovesTheFile(t *testing.T) {
	isolatedCredentials(t)
	var revokes int32
	newSrv := func() *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/auth/logout" {
				atomic.AddInt32(&revokes, 1)
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(s.Close)
		return s
	}
	a, b := newSrv(), newSrv()
	loginTo(t, a.URL, "shk_a", "a")
	loginTo(t, b.URL, "shk_b", "b")

	cmd := newLogoutCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&strings.Builder{})
	if err := runLogoutWith(cmd, &logoutFlags{all: true}); err != nil {
		t.Fatalf("logout --all: %v", err)
	}

	if got := atomic.LoadInt32(&revokes); got != 2 {
		t.Errorf("revoke calls = %d, want one per saved server (2)", got)
	}
	var res struct {
		Hosts              []string `json:"hosts"`
		RemainingHosts     int      `json:"remaining_hosts"`
		CredentialsRemoved bool     `json:"credentials_removed"`
	}
	if err := json.Unmarshal([]byte(out.String()), &res); err != nil {
		t.Fatalf("not an action envelope: %q (%v)", out.String(), err)
	}
	if len(res.Hosts) != 2 || res.RemainingHosts != 0 || !res.CredentialsRemoved {
		t.Errorf("got %+v, want both hosts listed, none remaining, file removed", res)
	}
	if _, err := os.Stat(configPath()); !os.IsNotExist(err) {
		t.Errorf("credentials file should be gone after --all, stat err: %v", err)
	}
}

// One unreachable server must not prevent the rest from being signed out - the
// user asked to log out everywhere.
func TestLogout_AllContinuesPastAnUnreachableServer(t *testing.T) {
	isolatedCredentials(t)
	var revokes int32
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&revokes, 1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(live.Close)

	if err := saveConfig(&cliConfig{Host: "http://127.0.0.1:1", Token: "shk_dead"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := saveConfig(&cliConfig{Host: live.URL, Token: "shk_live"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cmd := newLogoutCmd()
	var stderr strings.Builder
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&stderr)
	if err := runLogoutWith(cmd, &logoutFlags{all: true}); err != nil {
		t.Fatalf("logout --all should succeed despite an unreachable server: %v", err)
	}
	if got := atomic.LoadInt32(&revokes); got != 1 {
		t.Errorf("the reachable server was not asked to revoke (calls=%d)", got)
	}
	if !strings.Contains(stderr.String(), "127.0.0.1:1") {
		t.Errorf("stderr should name the server that could not be reached, got %q", stderr.String())
	}
	if _, err := os.Stat(configPath()); !os.IsNotExist(err) {
		t.Errorf("credentials file should still be removed, stat err: %v", err)
	}
}

func TestLogout_AllWithNothingSavedIsANoOp(t *testing.T) {
	isolatedCredentials(t)
	cmd := newLogoutCmd()
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	if err := runLogoutWith(cmd, &logoutFlags{all: true}); err != nil {
		t.Fatalf("logout --all with nothing saved should be a no-op, got %v", err)
	}
}

// whoami's job is to say which server you are on, so a dial failure that does
// not name the server answers everything except the question asked.
func TestWhoami_TransportErrorNamesTheHostAndPointsAtHosts(t *testing.T) {
	isolatedCredentials(t)
	if err := saveConfig(&cliConfig{Host: "http://127.0.0.1:1", Token: "shk_dead"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cmd := newWhoamiCmd()
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error against an unreachable server")
	}
	if !strings.Contains(err.Error(), "http://127.0.0.1:1") {
		t.Errorf("error should name the server it could not reach, got: %v", err)
	}
	// The advice belongs in the envelope's hint field, not folded into the
	// message: a caller parsing the envelope should not have to read prose to
	// find the next step.
	if hint := hintOf(err); !strings.Contains(hint, "shinyhub hosts") {
		t.Errorf("hint should point at the offline listing, got %q", hint)
	}
	// An unreachable server is a transport failure, not an auth or internal
	// one; retrying is meaningful, so it has to carry the retryable kind.
	if kind, code := classify(err); kind != KindNetwork || code != 3 {
		t.Errorf("got kind=%q code=%d, want %q/3", kind, code, KindNetwork)
	}
}

// A logout target that does not resolve is a mistake, not an idempotent no-op.
// Reporting "Not logged in." for a mistyped name exits 0 while the credential
// stays on disk, which is the worst possible answer: the user believes they
// signed out of a server they are still signed in to.
func TestLogout_UnresolvableTargetIsAnErrorNotASilentNoOp(t *testing.T) {
	cases := []struct {
		name     string
		selector string
	}{
		{"mistyped name", "prodd"},
		{"url without a scheme", "prod.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolatedCredentials(t)
			seedHosts(t, [3]string{"https://prod.example.com", "shk_prod", "prod"})
			hostFlagOverride = tc.selector
			t.Cleanup(func() { hostFlagOverride = "" })

			_, _, err := runCmd(t, newLogoutCmd())
			if err == nil {
				t.Fatalf("expected an error for --host %q", tc.selector)
			}
			if kind, _ := classify(err); kind != KindValidation {
				t.Errorf("kind = %q, want %q", kind, KindValidation)
			}
			st, _ := loadStore()
			if len(st.Hosts) != 1 {
				t.Errorf("a refused logout must not remove anything; %d hosts remain", len(st.Hosts))
			}
		})
	}
}

// Having nothing to sign out of stays an idempotent success. This is the other
// bound on the test above: propagating resolve failures must not turn the
// benign case into an error.
func TestLogout_NothingSavedRemainsASuccess(t *testing.T) {
	isolatedCredentials(t)

	stdout, _, err := runCmd(t, newLogoutCmd())
	if err != nil {
		t.Fatalf("logout with nothing saved should succeed: %v", err)
	}
	if !strings.Contains(stdout, "Not logged in") {
		t.Errorf("got %q, want it to say there was nothing to do", stdout)
	}
}

// `--host` names one server and `--all` means every server. Run together the
// destructive reading wins, so someone who meant to sign out of one server
// signs out everywhere and only finds out later.
func TestLogout_AllRefusesAContradictoryHostFlag(t *testing.T) {
	isolatedCredentials(t)
	seedHosts(t,
		[3]string{"https://prod.example.com", "shk_prod", "prod"},
		[3]string{"https://dev.example.com", "shk_dev", "dev"},
	)
	hostFlagOverride = "prod"
	t.Cleanup(func() { hostFlagOverride = "" })

	_, _, err := runCmd(t, newLogoutCmd(), "--all")
	if err == nil {
		t.Fatal("expected --host with --all to be refused")
	}
	if hint := hintOf(err); !strings.Contains(hint, "logout --host prod") {
		t.Errorf("hint should show the single-server command, got %q", hint)
	}
	st, _ := loadStore()
	if len(st.Hosts) != 2 {
		t.Errorf("a refused logout must remove nothing; %d hosts remain", len(st.Hosts))
	}
}

// SHINYHUB_HOST is ambient - CI shells export it for every command - so it must
// not turn `logout --all` into an error the way an explicitly typed --host does.
func TestLogout_AllIgnoresAnAmbientEnvHost(t *testing.T) {
	isolatedCredentials(t)
	srv := meServer(t, "alice")
	seedHosts(t, [3]string{srv.URL, "shk_a", "a"})
	t.Setenv("SHINYHUB_HOST", srv.URL)

	if _, _, err := runCmd(t, newLogoutCmd(), "--all"); err != nil {
		t.Fatalf("logout --all with SHINYHUB_HOST set should succeed: %v", err)
	}
	st, _ := loadStore()
	if len(st.Hosts) != 0 {
		t.Errorf("got %d hosts remaining, want 0", len(st.Hosts))
	}
}
