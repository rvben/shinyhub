//go:build e2e

package e2e_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/proxy"
)

// TestE2E_ReplicasDistribution verifies that cookie-less requests are spread
// across all registered replicas (least-connections routing with round-robin
// tie-breaking) and that a client with a cookie jar sticks to a single replica.
//
// Scope: exercises the proxy + sticky-cookie layer with real HTTP backends.
// Does not boot the full deploy pipeline — that would require Python/R
// processes, which is out of scope for a fast E2E smoke test.
func TestE2E_ReplicasDistribution(t *testing.T) {
	// Stand up two tiny backends. Each responds with a distinct body so we can
	// tell which one served a given request.
	backend0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "replica-0") //nolint:errcheck
	}))
	defer backend0.Close()

	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "replica-1") //nolint:errcheck
	}))
	defer backend1.Close()

	prx := proxy.New()
	prx.SetPoolSize("demo", 2)
	if err := prx.RegisterReplica("demo", 0, backend0.URL); err != nil {
		t.Fatalf("register replica 0: %v", err)
	}
	if err := prx.RegisterReplica("demo", 1, backend1.URL); err != nil {
		t.Fatalf("register replica 1: %v", err)
	}

	// Mount the proxy behind a test server. The proxy expects /app/<slug>/...
	// paths; test requests use /app/demo/.
	front := httptest.NewServer(prx)
	defer front.Close()

	// --- Distribution: 30 cookie-less requests must reach both replicas. ---
	//
	// Each http.Get uses a one-shot client with no cookie storage, so the proxy
	// sees no sticky cookie and falls back to least-connections + round-robin
	// tie-breaking. With both replicas idle and equal, the rrCounter alternates,
	// ensuring traffic is spread across both backends.
	hits := map[string]int{}
	for i := range 30 {
		resp, err := http.Get(front.URL + "/app/demo/")
		if err != nil {
			t.Fatalf("distribution req %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		hits[string(body)]++
	}
	if hits["replica-0"] == 0 || hits["replica-1"] == 0 {
		t.Fatalf("expected both replicas to receive traffic; distribution: %+v", hits)
	}
	t.Logf("distribution: replica-0=%d replica-1=%d", hits["replica-0"], hits["replica-1"])

	// --- Sticky: a single client (with a cookie jar) must land on one replica ---
	//
	// The proxy sets shinyhub_rep_<slug> on the first response. Subsequent
	// requests from the same client carry that cookie and are pinned to the
	// same backend.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}

	var first string
	for i := range 5 {
		resp, err := client.Get(front.URL + "/app/demo/")
		if err != nil {
			t.Fatalf("sticky req %d: %v", i, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		got := string(body)
		if first == "" {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("sticky session broken at request %d: started on %q, now on %q", i, first, got)
		}
	}
	t.Logf("sticky: all 5 requests landed on %q", first)

	// Sanity-check: the sticky cookie must be named shinyhub_rep_demo.
	stickyURL := mustParseURL(t, front.URL+"/app/demo/")
	cookies := jar.Cookies(stickyURL)
	found := false
	for _, c := range cookies {
		if c.Name == "shinyhub_rep_demo" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected shinyhub_rep_demo cookie in jar; got %+v", cookies)
	}
}
