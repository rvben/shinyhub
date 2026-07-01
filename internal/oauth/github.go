package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

// GitHubUser holds the fields we need from the GitHub API /user endpoint.
type GitHubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"` // GitHub username
	Name  string `json:"name"`  // Display name (may be empty)
	Email string `json:"email"` // Public profile email; null when private. FetchUser backfills it from /user/emails.
}

// GitHub is an OAuth2 provider for GitHub.
type GitHub struct {
	cfg *oauth2.Config
	// apiBase is the GitHub REST API root, overridable in tests. Defaults to
	// https://api.github.com.
	apiBase string
}

// NewGitHub creates a GitHub OAuth2 provider. callbackURL must match the
// redirect URI registered in the GitHub OAuth App settings.
func NewGitHub(clientID, clientSecret, callbackURL string) *GitHub {
	return &GitHub{
		cfg: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  callbackURL,
			Scopes:       []string{"read:user", "user:email"},
			Endpoint:     github.Endpoint,
		},
		apiBase: "https://api.github.com",
	}
}

// AuthURL returns the GitHub authorization URL. state is a CSRF nonce that
// must be stored (in the DB) and verified in the callback.
func (g *GitHub) AuthURL(state string) string {
	return g.cfg.AuthCodeURL(state, oauth2.AccessTypeOnline)
}

// Exchange trades the authorization code for an access token.
func (g *GitHub) Exchange(ctx context.Context, code string) (*oauth2.Token, error) {
	tok, err := g.cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("github token exchange: %w", err)
	}
	return tok, nil
}

// FetchUser retrieves the authenticated GitHub user's profile.
func (g *GitHub) FetchUser(ctx context.Context, tok *oauth2.Token) (*GitHubUser, error) {
	client := g.cfg.Client(ctx, tok)
	resp, err := client.Get(g.apiBase + "/user")
	if err != nil {
		return nil, fmt.Errorf("github user fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github user fetch: status %d", resp.StatusCode)
	}
	var u GitHubUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, fmt.Errorf("github user decode: %w", err)
	}
	if u.ID == 0 {
		return nil, fmt.Errorf("github user fetch: missing id in response")
	}
	// /user only exposes the public profile email, which is null when the user
	// keeps their email private. Fall back to the verified primary from
	// /user/emails (granted by the user:email scope) so private-email accounts
	// still get an identity email. Best-effort: a failure leaves it empty rather
	// than blocking login.
	if u.Email == "" {
		u.Email = g.fetchPrimaryEmail(client)
	}
	return &u, nil
}

// fetchPrimaryEmail returns the user's verified primary email from
// /user/emails, or the first verified address if none is flagged primary. It
// never returns an unverified address (that is not a trustworthy identity) and
// returns "" on any error or when no verified address exists.
func (g *GitHub) fetchPrimaryEmail(client *http.Client) string {
	resp, err := client.Get(g.apiBase + "/user/emails")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return ""
	}
	var firstVerified string
	for _, e := range emails {
		if !e.Verified {
			continue
		}
		if e.Primary {
			return e.Email
		}
		if firstVerified == "" {
			firstVerified = e.Email
		}
	}
	return firstVerified
}
