//go:build e2e

package e2e_test

import (
	"net/url"
	"testing"
)

// mustParseURL parses rawURL and fails the test if parsing fails.
func mustParseURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("mustParseURL(%q): %v", rawURL, err)
	}
	return u
}
