package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

// EnableEntraGroups requests least-privilege delegated membership access.
func (p *OIDCProvider) EnableEntraGroups() {
	p.oauth2Cfg.Scopes = append(p.oauth2Cfg.Scopes, "https://graph.microsoft.com/User.Read")
}

// EntraGroups never follows token-provided claim-source URLs or redirects.
// Only group IDs are used, so limited Graph object information is sufficient.
func (p *OIDCProvider) EntraGroups(ctx context.Context, token *oauth2.Token) ([]string, error) {
	if token == nil || token.AccessToken == "" {
		return nil, fmt.Errorf("Graph access token is missing")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if c, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok {
		client.Transport = c.Transport
	}
	next := "https://graph.microsoft.com/v1.0/me/transitiveMemberOf?$select=id&$top=999"
	groups := []string{}
	for page := 0; next != ""; page++ {
		if page >= 32 {
			return nil, fmt.Errorf("Graph group membership exceeded pagination limit")
		}
		u, err := url.Parse(next)
		if err != nil || u.Scheme != "https" || u.Host != "graph.microsoft.com" || !graphMembershipPath(u.Path) || u.User != nil || u.Fragment != "" {
			return nil, fmt.Errorf("Graph returned an invalid pagination URL")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "ShinyHub")
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("Graph memberships unavailable")
		}
		body, e := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
		resp.Body.Close()
		if e != nil || resp.StatusCode != http.StatusOK || len(body) > 1024*1024 {
			return nil, fmt.Errorf("Graph memberships request failed (HTTP %d)", resp.StatusCode)
		}
		var data struct {
			Value []struct {
				ID   string `json:"id"`
				Type string `json:"@odata.type"`
			} `json:"value"`
			Next string `json:"@odata.nextLink"`
		}
		if err := json.Unmarshal(body, &data); err != nil || data.Value == nil {
			return nil, fmt.Errorf("Graph memberships response is malformed")
		}
		for _, v := range data.Value {
			if v.Type == "" {
				return nil, fmt.Errorf("Graph membership object type is missing")
			}
			if v.Type == "#microsoft.graph.group" {
				if v.ID == "" {
					return nil, fmt.Errorf("Graph returned a group without an ID")
				}
				groups = append(groups, v.ID)
			}
		}
		next = data.Next
	}
	return groups, nil
}

func graphMembershipPath(path string) bool {
	if path == "/v1.0/me/transitiveMemberOf" {
		return true
	}
	parts := strings.Split(path, "/")
	if len(parts) != 5 || parts[1] != "v1.0" || parts[2] != "users" || parts[4] != "transitiveMemberOf" {
		return false
	}
	_, err := uuid.Parse(parts[3])
	return err == nil
}
