package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestClientID_NoCookie verifies that a request with no cid cookie produces
// isNew=true and a valid 32-character hex client id.
func TestClientID_NoCookie(t *testing.T) {
	p := New()
	p.SetStickySecret([]byte("test-secret-key-for-client-id"))

	r := httptest.NewRequest(http.MethodGet, "/app/myapp/", nil)
	id, isNew := p.clientID(r, "myapp")

	if !isNew {
		t.Error("isNew should be true when no cid cookie is present")
	}
	if len(id) != 32 {
		t.Errorf("id length = %d, want 32 hex chars; got %q", len(id), id)
	}
	// Must be valid hex.
	for _, ch := range id {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			t.Errorf("id contains non-hex character %q; full id: %q", ch, id)
		}
	}
}

// TestClientID_ValidCookie verifies that a request with a valid signed cid
// cookie returns the stored id with isNew=false.
func TestClientID_ValidCookie(t *testing.T) {
	p := New()
	key := []byte("test-secret-key-for-client-id")
	p.SetStickySecret(key)

	// Build a valid signed value using the same function under test.
	const slug = "myapp"
	const knownID = "aabbccddeeff00112233445566778899"
	signed := signClientValue(key, slug, knownID)

	r := httptest.NewRequest(http.MethodGet, "/app/"+slug+"/", nil)
	r.AddCookie(&http.Cookie{
		Name:  clientCookiePrefix + slug,
		Value: signed,
	})

	id, isNew := p.clientID(r, slug)

	if isNew {
		t.Error("isNew should be false for a valid signed cid cookie")
	}
	if id != knownID {
		t.Errorf("id = %q, want %q", id, knownID)
	}
}

// TestClientID_TamperedCookie verifies that a request with a tampered cid
// cookie is treated as new (isNew=true) and a fresh id is generated.
func TestClientID_TamperedCookie(t *testing.T) {
	p := New()
	key := []byte("test-secret-key-for-client-id")
	p.SetStickySecret(key)

	const slug = "myapp"
	const knownID = "aabbccddeeff00112233445566778899"
	signed := signClientValue(key, slug, knownID)
	// Tamper: flip the last character of the HMAC segment.
	tampered := signed[:len(signed)-1] + "x"

	r := httptest.NewRequest(http.MethodGet, "/app/"+slug+"/", nil)
	r.AddCookie(&http.Cookie{
		Name:  clientCookiePrefix + slug,
		Value: tampered,
	})

	id, isNew := p.clientID(r, slug)

	if !isNew {
		t.Error("isNew should be true for a tampered cid cookie")
	}
	// Fresh id must be non-empty and 32 hex chars.
	if len(id) != 32 {
		t.Errorf("fresh id length = %d, want 32; got %q", len(id), id)
	}
	if id == knownID {
		t.Error("tampered cookie should not return the original id")
	}
}

// TestSetClientCookie verifies that setClientCookie writes a cookie with the
// correct name, path, HttpOnly=true, and SameSite=Lax.
func TestSetClientCookie(t *testing.T) {
	p := New()
	p.SetStickySecret([]byte("test-secret-key-for-client-id"))

	const slug = "myapp"
	const id = "aabbccddeeff00112233445566778899"

	rec := httptest.NewRecorder()
	p.setClientCookie(rec, slug, id)

	cookies := rec.Result().Cookies()
	var found *http.Cookie
	for _, c := range cookies {
		if c.Name == clientCookiePrefix+slug {
			found = c
			break
		}
	}
	if found == nil {
		t.Fatalf("cookie %q not set", clientCookiePrefix+slug)
	}
	if found.Path != "/app/"+slug+"/" {
		t.Errorf("Path = %q, want %q", found.Path, "/app/"+slug+"/")
	}
	if !found.HttpOnly {
		t.Error("HttpOnly should be true")
	}
	if found.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", found.SameSite)
	}
	// Value must contain the id.
	if !strings.Contains(found.Value, id) {
		t.Errorf("cookie value %q does not contain id %q", found.Value, id)
	}
}

// TestClientID_NoSecret_UnsignedFallback verifies that when no secret is
// configured, the bare id (no HMAC suffix) is written and read back correctly.
func TestClientID_NoSecret_UnsignedFallback(t *testing.T) {
	p := New()
	// No SetStickySecret call - unsigned mode.

	const slug = "myapp"
	const knownID = "aabbccddeeff00112233445566778899"

	r := httptest.NewRequest(http.MethodGet, "/app/"+slug+"/", nil)
	r.AddCookie(&http.Cookie{
		Name:  clientCookiePrefix + slug,
		Value: knownID, // bare id, no signature
	})

	id, isNew := p.clientID(r, slug)

	if isNew {
		t.Error("isNew should be false for a valid unsigned cid cookie in unsigned mode")
	}
	if id != knownID {
		t.Errorf("id = %q, want %q", id, knownID)
	}
}
