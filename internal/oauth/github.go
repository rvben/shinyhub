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
	Email string `json:"email"` // May be empty if not public
}

// GitHub is an OAuth2 provider for GitHub.
type GitHub struct {
	cfg *oauth2.Config
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
	resp, err := client.Get("https://api.github.com/user")
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
	return &u, nil
}
