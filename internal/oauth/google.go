package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// GoogleUser holds the fields we need from the Google userinfo endpoint.
type GoogleUser struct {
	ID    string `json:"id"`    // Stable numeric user ID (as string)
	Email string `json:"email"` // Always present and verified
	Name  string `json:"name"`  // Display name (may be empty)
}

// Google is an OAuth2 provider for Google.
type Google struct {
	cfg *oauth2.Config
}

// NewGoogle creates a Google OAuth2 provider. callbackURL must match the
// redirect URI registered in the Google Cloud Console OAuth2 credentials.
func NewGoogle(clientID, clientSecret, callbackURL string) *Google {
	return &Google{
		cfg: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  callbackURL,
			Scopes:       []string{"openid", "email", "profile"},
			Endpoint:     google.Endpoint,
		},
	}
}

// AuthURL returns the Google authorization URL. state is a CSRF nonce that
// must be stored (in the DB) and verified in the callback.
func (g *Google) AuthURL(state string) string {
	return g.cfg.AuthCodeURL(state, oauth2.AccessTypeOnline)
}

// Exchange trades the authorization code for an access token.
func (g *Google) Exchange(ctx context.Context, code string) (*oauth2.Token, error) {
	tok, err := g.cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("google token exchange: %w", err)
	}
	return tok, nil
}

// FetchUser retrieves the authenticated Google user's profile.
func (g *Google) FetchUser(ctx context.Context, tok *oauth2.Token) (*GoogleUser, error) {
	client := g.cfg.Client(ctx, tok)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v2/userinfo")
	if err != nil {
		return nil, fmt.Errorf("google user fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google user fetch: status %d", resp.StatusCode)
	}
	var u GoogleUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, fmt.Errorf("google user decode: %w", err)
	}
	if u.ID == "" {
		return nil, fmt.Errorf("google user fetch: missing id in response")
	}
	if u.Email == "" {
		return nil, fmt.Errorf("google user fetch: missing email in response")
	}
	return &u, nil
}
