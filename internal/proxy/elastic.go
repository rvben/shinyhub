package proxy

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/proxytrust"
)

// clientCookiePrefix is the name prefix for the per-slug client-id cookie.
// The full name is clientCookiePrefix + slug.
const clientCookiePrefix = "shinyhub_cid_"

// signClientValue returns the signed client-cookie value "<idhex>.<hmac16>".
// The HMAC-SHA256 binds slug and idhex so a value cannot be replayed across
// apps or with a modified id.
func signClientValue(key []byte, slug, idhex string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(slug))
	mac.Write([]byte{0x00})
	mac.Write([]byte(idhex))
	return idhex + "." + hex.EncodeToString(mac.Sum(nil))[:16]
}

// verifyClientValue parses and authenticates a signed client-cookie value.
// Returns (idhex, true) when the value has the expected format and the HMAC
// matches; otherwise returns ("", false).
func verifyClientValue(key []byte, slug, value string) (string, bool) {
	idhex, _, found := strings.Cut(value, ".")
	if !found {
		return "", false
	}
	// Recompute the expected signed form and compare with hmac.Equal to
	// prevent timing-based side-channel attacks.
	expected := signClientValue(key, slug, idhex)
	if !hmac.Equal([]byte(value), []byte(expected)) {
		return "", false
	}
	return idhex, true
}

// clientID returns the stable client identifier for this request and slug.
// When the request carries a valid signed (or, in unsigned mode, bare) cid
// cookie the stored id is returned with isNew=false. When absent or tampered,
// a fresh random 128-bit id (32 hex chars) is returned with isNew=true.
func (p *Proxy) clientID(r *http.Request, slug string) (id string, isNew bool) {
	cookieName := clientCookiePrefix + slug
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		if key := p.stickySecretBytes(); len(key) > 0 {
			if idhex, ok := verifyClientValue(key, slug, c.Value); ok {
				return idhex, false
			}
			// Tampered or invalid - fall through to generate a new id.
		} else {
			// Unsigned mode: accept a bare 32-hex-char id (no signature).
			if len(c.Value) == 32 {
				return c.Value, false
			}
		}
	}

	// Generate a fresh random 128-bit id.
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is extremely rare; panic is appropriate here
		// because the system's entropy source is broken.
		panic("proxy: crypto/rand failure: " + err.Error())
	}
	return hex.EncodeToString(buf), true
}

// setClientCookie writes the client-id cookie onto w. The cookie value is
// signed when a sticky secret is configured; otherwise the bare id is stored.
// Cookie attributes (Path, HttpOnly, SameSite, Secure) mirror the sticky
// routing cookie exactly: Secure is set when the request was received over
// HTTPS (as determined by X-Forwarded-Proto, trusted only from configured
// proxy CIDRs).
func (p *Proxy) setClientCookie(w http.ResponseWriter, r *http.Request, slug, id string) {
	var value string
	if key := p.stickySecretBytes(); len(key) > 0 {
		value = signClientValue(key, slug, id)
	} else {
		value = id
	}
	http.SetCookie(w, &http.Cookie{
		Name:     clientCookiePrefix + slug,
		Value:    value,
		Path:     "/app/" + slug + "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Mirror the sticky routing cookie's scheme-aware policy: Secure over
		// HTTPS so the cookie is never sent in cleartext. X-Forwarded-Proto is
		// trusted only from configured proxy CIDRs.
		Secure: proxytrust.Scheme(r, p.trustedProxyNets()) == "https",
	})
}

// poolIsElastic reports whether pool is in demand-driven (elastic) mode.
// A pool created by an existing SetPoolSize/SetPoolCap caller has a zero-value
// mode (""), which is intentionally treated as multiplex so the existing
// single-slot behaviour is byte-for-byte unchanged.
func poolIsElastic(pool *backendPool) bool {
	return pool.mode == config.IsolationGrouped || pool.mode == config.IsolationPerSession
}

// workerStates returns a snapshot of every worker in the elastic pool as a
// []workerState that the pure decide() function consumes. Callers must hold
// the pool lock (p.mu) for the duration of the call.
func (pool *backendPool) workerStates() []workerState {
	if len(pool.workers) == 0 {
		return nil
	}
	out := make([]workerState, 0, len(pool.workers))
	for _, w := range pool.workers {
		out = append(out, workerState{
			slotID:          w.slotID,
			assignedClients: w.assignedClients,
			status:          w.status,
		})
	}
	return out
}

// allocateSlotID returns the next monotonically increasing slot ID for this
// pool. IDs are never reused within a pool's lifetime, so a routing pin
// referencing a removed slot is always stale. Callers must hold the pool lock.
func (pool *backendPool) allocateSlotID() int {
	id := pool.nextSlotID
	pool.nextSlotID++
	return id
}

// addElasticWorker inserts a replicaBackend into the elastic workers map.
// r.slotID must already be set (via allocateSlotID). Callers must hold the
// pool lock.
func addElasticWorker(pool *backendPool, r *replicaBackend) {
	if pool.workers == nil {
		pool.workers = make(map[int]*replicaBackend)
	}
	pool.workers[r.slotID] = r
}

// removeElasticWorker removes the worker identified by slotID from the elastic
// workers map. It is a no-op for unknown slot IDs. Callers must hold the pool
// lock.
func removeElasticWorker(pool *backendPool, slotID int) {
	delete(pool.workers, slotID)
}
