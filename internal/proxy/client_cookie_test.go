package proxy

import (
	"crypto/tls"
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
	// No user in context -> clientID uses the "anon" tag, so sign with it.
	signed := signClientValue(key, slug, "anon", knownID)

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
	signed := signClientValue(key, slug, "anon", knownID)
	// Tamper: replace the 16-char HMAC segment with all-zeros so the mutation
	// is always different from the real MAC, regardless of what it happens to be.
	dot := strings.Index(signed, ".")
	tampered := signed[:dot+1] + "0000000000000000"

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
// correct name, path, HttpOnly=true, SameSite=Lax, and Secure mirroring the
// request scheme (matching the sticky routing cookie policy exactly).
func TestSetClientCookie(t *testing.T) {
	p := New()
	p.SetStickySecret([]byte("test-secret-key-for-client-id"))

	const slug = "myapp"
	const id = "aabbccddeeff00112233445566778899"

	findCookie := func(rec *httptest.ResponseRecorder) *http.Cookie {
		t.Helper()
		for _, c := range rec.Result().Cookies() {
			if c.Name == clientCookiePrefix+slug {
				return c
			}
		}
		return nil
	}

	// HTTP request -> Secure must be false (Secure over HTTPS only, mirrors
	// the sticky routing cookie's scheme-aware policy).
	t.Run("http_request_not_secure", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "http://localhost/app/"+slug+"/", nil)
		p.setClientCookie(rec, r, slug, id)

		found := findCookie(rec)
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
		if found.Secure {
			t.Error("Secure should be false for a plain http:// request")
		}
		if !strings.Contains(found.Value, id) {
			t.Errorf("cookie value %q does not contain id %q", found.Value, id)
		}
	})

	// TLS request -> Secure must be true. r.TLS != nil is the authoritative
	// signal for direct HTTPS connections (checked before X-Forwarded-Proto).
	t.Run("tls_request_is_secure", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "https://localhost/app/"+slug+"/", nil)
		r.TLS = &tls.ConnectionState{}
		p.setClientCookie(rec, r, slug, id)

		found := findCookie(rec)
		if found == nil {
			t.Fatalf("cookie %q not set", clientCookiePrefix+slug)
		}
		if !found.Secure {
			t.Error("Secure should be true for a TLS request")
		}
	})
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
