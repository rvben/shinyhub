package oauth

import (
	"context"
	"golang.org/x/oauth2"
	"io"
	"net/http"
	"strings"
	"testing"
)

type graphTransport func(*http.Request) (*http.Response, error)

func (f graphTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEntraGroupsPaginationAndRequestBoundary(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: graphTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Host != "graph.microsoft.com" || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("User-Agent") != "ShinyHub" {
			t.Fatalf("unexpected request: %v %v", r.URL, r.Header)
		}
		body := `{"value":[{"@odata.type":"#microsoft.graph.group","id":"a"},{"@odata.type":"#microsoft.graph.directoryRole","id":"ignored"}],"@odata.nextLink":"https://graph.microsoft.com/v1.0/users/00000000-0000-4000-8000-000000000001/transitiveMemberOf?$skiptoken=next"}`
		if requests == 2 {
			body = `{"value":[{"@odata.type":"#microsoft.graph.group","id":"b"}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	groups, err := new(OIDCProvider).EntraGroups(context.WithValue(context.Background(), oauth2.HTTPClient, client), &oauth2.Token{AccessToken: "test-token"})
	if err != nil || strings.Join(groups, ",") != "a,b" || requests != 2 {
		t.Fatalf("groups=%v requests=%d err=%v", groups, requests, err)
	}
}

func TestEntraGroupsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"other host", `{"value":[],"@odata.nextLink":"https://evil.example/members"}`, 200},
		{"other path", `{"value":[],"@odata.nextLink":"https://graph.microsoft.com/v1.0/users"}`, 200},
		{"invalid JSON", `{`, 200},
		{"missing ID", `{"value":[{"@odata.type":"#microsoft.graph.group"}]}`, 200},
		{"access denied", `{"error":"denied"}`, 403},
		{"redirect", ``, 302},
		{"oversized", strings.Repeat(" ", 1024*1024+1), 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			client := &http.Client{Transport: graphTransport(func(r *http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{"Location": []string{"https://evil.example"}}}, nil
			})}
			groups, err := new(OIDCProvider).EntraGroups(context.WithValue(context.Background(), oauth2.HTTPClient, client), &oauth2.Token{AccessToken: "test-token"})
			if err == nil || groups != nil || requests != 1 {
				t.Fatalf("unsafe result: %v %v requests=%d", groups, err, requests)
			}
		})
	}
	if _, err := new(OIDCProvider).EntraGroups(context.Background(), nil); err == nil {
		t.Fatal("missing token accepted")
	}
}

func TestEntraGroupsRequestsDelegatedScope(t *testing.T) {
	p := &OIDCProvider{oauth2Cfg: oauth2.Config{Endpoint: oauth2.Endpoint{AuthURL: "https://login.example/authorize"}}}
	p.EnableEntraGroups()
	if !strings.Contains(p.AuthURL("state", "nonce"), "User.Read") {
		t.Fatal("Graph scope missing")
	}
}
