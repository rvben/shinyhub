package proxy_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/proxy"
)

// TestDeploymentStamp_MatchingIDStickyHit verifies that a cookie carrying a
// matching (idx, deploymentID) routes to the pinned replica and is counted as
// a sticky hit (no new Set-Cookie on the response).
func TestDeploymentStamp_MatchingIDStickyHit(t *testing.T) {
	var hits0, hits1 int
	b0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits0++
		fmt.Fprint(w, "rep0")
	}))
	b1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits1++
		fmt.Fprint(w, "rep1")
	}))
	defer b0.Close()
	defer b1.Close()

	p := proxy.New()
	const depID int64 = 77
	p.SetPoolSize("demo", 2)
	// Register replica 1 with a known deploymentID.
	_ = p.RegisterReplica("demo", 0, b0.URL, nil, depID)
	_ = p.RegisterReplica("demo", 1, b1.URL, nil, depID)

	// First request: no cookie — gets assigned a fresh cookie.
	req1 := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	rec1 := httptest.NewRecorder()
	p.ServeHTTP(rec1, req1)
	var cookieVal string
	for _, c := range rec1.Result().Cookies() {
		if c.Name == "shinyhub_rep_demo" {
			cookieVal = c.Value
		}
	}
	if cookieVal == "" {
		t.Fatal("expected sticky cookie on first response")
	}

	firstBody := rec1.Body.String()

	// Second request: present the same cookie — must stick and not re-issue.
	req2 := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	req2.AddCookie(&http.Cookie{Name: "shinyhub_rep_demo", Value: cookieVal})
	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, req2)

	if rec2.Body.String() != firstBody {
		t.Errorf("sticky hit failed: first=%q second=%q", firstBody, rec2.Body.String())
	}
	// On a sticky hit no new Set-Cookie should be emitted.
	for _, c := range rec2.Result().Cookies() {
		if c.Name == "shinyhub_rep_demo" {
			t.Errorf("unexpected Set-Cookie on sticky hit: %q", c.Value)
		}
	}
}

// TestDeploymentStamp_StaleMismatchRepicks verifies that a cookie with an old
// deploymentID causes a re-pick (not a sticky hit) and the response carries a
// fresh cookie with the current deploymentID.
func TestDeploymentStamp_StaleMismatchRepicks(t *testing.T) {
	b0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "rep0")
	}))
	b1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "rep1")
	}))
	defer b0.Close()
	defer b1.Close()

	p := proxy.New()
	p.SetPoolSize("demo", 2)
	const oldDepID int64 = 10
	const newDepID int64 = 20
	// Register both replicas with the old deployment ID to get a valid cookie.
	_ = p.RegisterReplica("demo", 0, b0.URL, nil, oldDepID)
	_ = p.RegisterReplica("demo", 1, b1.URL, nil, oldDepID)

	// First request: no cookie — picks a replica via least-connections and issues a cookie.
	req1 := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	rec1 := httptest.NewRecorder()
	p.ServeHTTP(rec1, req1)
	var oldCookie string
	for _, c := range rec1.Result().Cookies() {
		if c.Name == "shinyhub_rep_demo" {
			oldCookie = c.Value
		}
	}
	if oldCookie == "" {
		t.Fatal("expected cookie on first response")
	}

	// Simulate a redeploy: re-register both replicas with a new deployment ID.
	_ = p.RegisterReplica("demo", 0, b0.URL, nil, newDepID)
	_ = p.RegisterReplica("demo", 1, b1.URL, nil, newDepID)

	// Present the stale cookie — must re-pick (deployment mismatch) and re-issue.
	req2 := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	req2.AddCookie(&http.Cookie{Name: "shinyhub_rep_demo", Value: oldCookie})
	rec2 := httptest.NewRecorder()
	p.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec2.Code)
	}

	// A new cookie must be set with the new deploymentID embedded.
	var newCookie string
	for _, c := range rec2.Result().Cookies() {
		if c.Name == "shinyhub_rep_demo" {
			newCookie = c.Value
		}
	}
	if newCookie == "" {
		t.Fatal("expected a new Set-Cookie after deployment mismatch re-pick")
	}
	if newCookie == oldCookie {
		t.Errorf("re-issued cookie must differ from the stale one; got same value %q", newCookie)
	}
	// In unsigned mode the format is "<idx>.<depID>"; in signed mode it is
	// "<idx>.<depID>.<hmac>". Either way the deploymentID must appear as a
	// dot-separated component.
	newDepStr := fmt.Sprintf("%d", newDepID)
	if !strings.Contains(newCookie, "."+newDepStr+".") && !strings.HasSuffix(newCookie, "."+newDepStr) {
		t.Errorf("new cookie %q does not carry new deploymentID %d", newCookie, newDepID)
	}
}

// TestDeploymentStamp_OldTwoPartCookieRepicks verifies that an old 2-part
// "<idx>.<hmac>" cookie (format before deployment-stamping) is treated as stale
// and causes a re-pick without crashing.
func TestDeploymentStamp_OldTwoPartCookieRepicks(t *testing.T) {
	b0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "rep0")
	}))
	defer b0.Close()

	p := proxy.New()
	p.SetPoolSize("demo", 1)
	_ = p.RegisterReplica("demo", 0, b0.URL, nil, 1)

	// Present an old 2-part signed cookie.
	req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	req.AddCookie(&http.Cookie{Name: "shinyhub_rep_demo", Value: "0.deadbeefdeadbeef"})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (re-pick), got %d", rec.Code)
	}
	// A new cookie must be re-issued.
	var newCookie string
	for _, c := range rec.Result().Cookies() {
		if c.Name == "shinyhub_rep_demo" {
			newCookie = c.Value
		}
	}
	if newCookie == "" {
		t.Error("expected new Set-Cookie after stale 2-part cookie re-pick")
	}
}

// TestDeploymentStamp_BareIntegerCookieRepicks verifies that a bare integer
// cookie (old unsigned format) is treated as stale and causes a re-pick.
func TestDeploymentStamp_BareIntegerCookieRepicks(t *testing.T) {
	b0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "rep0")
	}))
	defer b0.Close()

	p := proxy.New()
	p.SetPoolSize("demo", 1)
	_ = p.RegisterReplica("demo", 0, b0.URL, nil, 5)

	// Present a bare integer cookie (old unsigned format).
	req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	req.AddCookie(&http.Cookie{Name: "shinyhub_rep_demo", Value: "0"})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (re-pick), got %d", rec.Code)
	}
	// A new cookie must be re-issued with the current format.
	var newCookie string
	for _, c := range rec.Result().Cookies() {
		if c.Name == "shinyhub_rep_demo" {
			newCookie = c.Value
		}
	}
	if newCookie == "" {
		t.Error("expected new Set-Cookie after bare-integer re-pick")
	}
	// The new cookie must not be a bare integer.
	if newCookie == "0" || newCookie == "1" {
		t.Errorf("new cookie should not be a bare integer, got %q", newCookie)
	}
}

// TestDeploymentStamp_HMACBindsAllThree verifies that tampering any of the
// three fields (index, deploymentID, hmac segment) in a signed cookie causes
// verification failure and a re-pick.
func TestDeploymentStamp_HMACBindsAllThree(t *testing.T) {
	p := proxy.New()
	p.SetStickySecret([]byte("test-sticky-secret-for-hmac-test"))

	b0 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "rep0")
	}))
	b1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "rep1")
	}))
	defer b0.Close()
	defer b1.Close()

	p.SetPoolSize("demo", 2)
	_ = p.RegisterReplica("demo", 0, b0.URL, nil, 100)
	_ = p.RegisterReplica("demo", 1, b1.URL, nil, 100)

	// Obtain a valid cookie.
	req0 := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	rec0 := httptest.NewRecorder()
	p.ServeHTTP(rec0, req0)
	var validCookie string
	for _, c := range rec0.Result().Cookies() {
		if c.Name == "shinyhub_rep_demo" {
			validCookie = c.Value
		}
	}
	if validCookie == "" {
		t.Fatal("expected a cookie from the first request")
	}

	// Parse: "<idx>.<depID>.<hmac>"
	parts := strings.SplitN(validCookie, ".", 3)
	if len(parts) != 3 {
		t.Fatalf("expected 3-part cookie, got %q", validCookie)
	}
	idx, depStr, sig := parts[0], parts[1], parts[2]

	tampered := []struct {
		name  string
		value string
	}{
		{"tampered-idx", "9." + depStr + "." + sig},
		{"tampered-depID", idx + ".9999." + sig},
		{"tampered-sig", idx + "." + depStr + ".0000000000000000"},
	}
	for _, tc := range tampered {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
			req.AddCookie(&http.Cookie{Name: "shinyhub_rep_demo", Value: tc.value})
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 (re-pick after tamper), got %d", rec.Code)
			}
			// Must re-issue a cookie (not a sticky hit).
			var newCookie string
			for _, c := range rec.Result().Cookies() {
				if c.Name == "shinyhub_rep_demo" {
					newCookie = c.Value
				}
			}
			if newCookie == "" {
				t.Errorf("expected new Set-Cookie after tampered cookie re-pick")
			}
			if newCookie == tc.value {
				t.Errorf("re-issued cookie should not equal the tampered value")
			}
		})
	}
}

// TestDeploymentStamp_CrossInstanceAffinity verifies that two Proxy instances
// sharing the same sticky secret and identical deployment IDs route the same
// cookie value to the same replica index. This is the cross-instance affinity
// primitive the HA phase relies on.
func TestDeploymentStamp_CrossInstanceAffinity(t *testing.T) {
	var hits0A, hits1A, hits0B, hits1B int
	b0a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits0A++ }))
	b1a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits1A++ }))
	b0b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits0B++ }))
	b1b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits1B++ }))
	defer b0a.Close()
	defer b1a.Close()
	defer b0b.Close()
	defer b1b.Close()

	const secret = "cross-instance-affinity-test-key"
	const depID int64 = 55

	// Instance A.
	pA := proxy.New()
	pA.SetStickySecret([]byte(secret))
	pA.SetPoolSize("demo", 2)
	_ = pA.RegisterReplica("demo", 0, b0a.URL, nil, depID)
	_ = pA.RegisterReplica("demo", 1, b1a.URL, nil, depID)

	// Instance B (same secret, same depID).
	pB := proxy.New()
	pB.SetStickySecret([]byte(secret))
	pB.SetPoolSize("demo", 2)
	_ = pB.RegisterReplica("demo", 0, b0b.URL, nil, depID)
	_ = pB.RegisterReplica("demo", 1, b1b.URL, nil, depID)

	// Get a cookie from instance A.
	reqA := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	recA := httptest.NewRecorder()
	pA.ServeHTTP(recA, reqA)
	var cookieFromA string
	for _, c := range recA.Result().Cookies() {
		if c.Name == "shinyhub_rep_demo" {
			cookieFromA = c.Value
		}
	}
	if cookieFromA == "" {
		t.Fatal("expected cookie from instance A")
	}

	// Use that cookie on instance B — must route to the same replica index.
	reqB := httptest.NewRequest(http.MethodGet, "/app/demo/", nil)
	reqB.AddCookie(&http.Cookie{Name: "shinyhub_rep_demo", Value: cookieFromA})
	recB := httptest.NewRecorder()
	pB.ServeHTTP(recB, reqB)

	// Instance A routed to exactly one of rep0 or rep1.
	var repA int
	switch {
	case hits0A == 1 && hits1A == 0:
		repA = 0
	case hits0A == 0 && hits1A == 1:
		repA = 1
	default:
		t.Fatalf("instance A: expected exactly one hit, got hits0=%d hits1=%d", hits0A, hits1A)
	}
	// Instance B must have routed to the same index.
	var repB int
	switch {
	case hits0B == 1 && hits1B == 0:
		repB = 0
	case hits0B == 0 && hits1B == 1:
		repB = 1
	default:
		t.Fatalf("instance B: expected exactly one hit, got hits0=%d hits1=%d", hits0B, hits1B)
	}

	if repA != repB {
		t.Errorf("cross-instance affinity failed: instance A pinned to %d but instance B routed to %d", repA, repB)
	}
	// Verify instance B treated it as a sticky hit (no new Set-Cookie).
	for _, c := range recB.Result().Cookies() {
		if c.Name == "shinyhub_rep_demo" {
			t.Errorf("instance B re-issued cookie on a cross-instance sticky hit: %q", c.Value)
		}
	}
}
