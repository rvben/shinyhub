package cli

import (
	"net/http"
	"strings"
	"testing"
)

func TestNewRunID_UniqueAndShaped(t *testing.T) {
	a, b := newRunID(), newRunID()
	if a == b {
		t.Fatal("run ids must be unique")
	}
	if len(a) != 32 {
		t.Fatalf("run id len = %d, want 32 hex chars", len(a))
	}
}

func TestDecorateFleetRequest_SetsRunAndUA(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://x/api/apps", nil)
	decorateFleetRequest(req, "run123")
	if req.Header.Get("X-Shinyhub-Run-Id") != "run123" {
		t.Fatalf("run id header = %q", req.Header.Get("X-Shinyhub-Run-Id"))
	}
	if ua := req.Header.Get("User-Agent"); !strings.HasPrefix(ua, "shinyhub-fleet/") {
		t.Fatalf("user-agent = %q", ua)
	}
}

func TestSetPrecondition_DigestAndManagedBy(t *testing.T) {
	// digest only
	r1, _ := http.NewRequest("PATCH", "http://x", nil)
	dg := "sha256:abc"
	setPrecondition(r1, &dg, nil)
	if r1.Header.Get("X-Shinyhub-If-Content-Digest") != "sha256:abc" {
		t.Fatalf("digest precondition = %q", r1.Header.Get("X-Shinyhub-If-Content-Digest"))
	}
	if _, ok := r1.Header["X-Shinyhub-If-Managed-By"]; ok {
		t.Fatal("managed-by header must be absent when nil")
	}
	// managed-by present-but-empty asserts "currently unmanaged"
	r2, _ := http.NewRequest("PATCH", "http://x", nil)
	empty := ""
	setPrecondition(r2, nil, &empty)
	if _, ok := r2.Header["X-Shinyhub-If-Managed-By"]; !ok {
		t.Fatal("managed-by header must be present (even empty) to assert unmanaged")
	}
	if r2.Header.Get("X-Shinyhub-If-Managed-By") != "" {
		t.Fatalf("managed-by = %q, want empty", r2.Header.Get("X-Shinyhub-If-Managed-By"))
	}
}

func TestIsConflict(t *testing.T) {
	if !isConflict(&http.Response{StatusCode: 409}) {
		t.Fatal("409 must be a conflict")
	}
	if isConflict(&http.Response{StatusCode: 200}) {
		t.Fatal("200 is not a conflict")
	}
}
