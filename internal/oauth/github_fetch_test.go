package oauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/oauth2"
)

// newGitHubStub returns a GitHub provider pointed at a stub API serving the
// given /user body and /user/emails body. A nil emailsBody makes /user/emails
// 404 (as when the endpoint is unreachable).
func newGitHubStub(t *testing.T, userBody, emailsBody string) (*GitHub, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, userBody)
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, _ *http.Request) {
		if emailsBody == "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		fmt.Fprint(w, emailsBody)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	g := NewGitHub("id", "secret", "http://localhost/cb")
	g.apiBase = srv.URL
	return g, srv
}

func fetch(t *testing.T, g *GitHub) *GitHubUser {
	t.Helper()
	u, err := g.FetchUser(context.Background(), &oauth2.Token{AccessToken: "t"})
	if err != nil {
		t.Fatalf("FetchUser: %v", err)
	}
	return u
}

// TestFetchUser_PrivateEmailUsesVerifiedPrimary is the core gap: /user returns a
// null email for a private-email account, so the verified primary from
// /user/emails must be used, otherwise X-Shinyhub-Email is never forwarded for
// these (common) GitHub users.
func TestFetchUser_PrivateEmailUsesVerifiedPrimary(t *testing.T) {
	g, _ := newGitHubStub(t,
		`{"id":42,"login":"octocat","name":"Octo","email":null}`,
		`[{"email":"secondary@x.example","primary":false,"verified":true},
		  {"email":"octo@x.example","primary":true,"verified":true},
		  {"email":"unverified@x.example","primary":false,"verified":false}]`)
	if got := fetch(t, g).Email; got != "octo@x.example" {
		t.Errorf("Email = %q, want the verified primary %q", got, "octo@x.example")
	}
}

// A public profile email is used directly; no /user/emails fallback needed.
func TestFetchUser_PublicEmailUsedDirectly(t *testing.T) {
	// /user/emails is served but should not override the present public email.
	g, _ := newGitHubStub(t,
		`{"id":42,"login":"octocat","name":"Octo","email":"public@x.example"}`,
		`[{"email":"other@x.example","primary":true,"verified":true}]`)
	if got := fetch(t, g).Email; got != "public@x.example" {
		t.Errorf("Email = %q, want the public profile email", got)
	}
}

// An unverified address is never adopted as identity; email stays empty.
func TestFetchUser_UnverifiedEmailNotAdopted(t *testing.T) {
	g, _ := newGitHubStub(t,
		`{"id":42,"login":"octocat","name":"Octo","email":null}`,
		`[{"email":"unverified@x.example","primary":true,"verified":false}]`)
	if got := fetch(t, g).Email; got != "" {
		t.Errorf("Email = %q, want empty (unverified must not be adopted)", got)
	}
}

// A failing /user/emails call is best-effort: login still succeeds, email empty.
func TestFetchUser_EmailsEndpointFailureIsNonFatal(t *testing.T) {
	g, _ := newGitHubStub(t,
		`{"id":42,"login":"octocat","name":"Octo","email":null}`, "")
	u := fetch(t, g)
	if u.Login != "octocat" {
		t.Errorf("Login = %q, want octocat (login must survive an emails failure)", u.Login)
	}
	if u.Email != "" {
		t.Errorf("Email = %q, want empty", u.Email)
	}
}
