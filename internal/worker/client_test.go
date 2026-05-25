// internal/worker/client_test.go
package worker

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientFetchBundleStreams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/bundles/sha256:abc" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte("zip-bytes"))
	}))
	defer srv.Close()

	// A plaintext-transport client is enough to exercise FetchBundle's request
	// shaping; the mTLS transport (cert presentation and server-cert pinning) is
	// exercised by TestClientMTLSRoundTrip.
	c := &Client{serverURL: srv.URL, httpc: srv.Client()}
	rc, err := c.FetchBundle(t.Context(), "sha256:abc")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	defer rc.Close()
	body, _ := io.ReadAll(rc)
	if string(body) != "zip-bytes" {
		t.Fatalf("body = %q", body)
	}
}
