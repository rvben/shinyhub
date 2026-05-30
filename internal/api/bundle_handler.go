package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/bundletoken"
	"github.com/rvben/shinyhub/internal/db"
)

// FargateBundleHandler serves bundle zips to Fargate tasks that authenticate
// with a short-lived HMAC capability token (Authorization: Bearer). It is
// mounted on GET /internal/fargate-bundle/{digest} directly on the main mux
// so large bundle streams are not subject to the apiTimeoutHandler's 30s cap.
// Per-source-IP rate limiting bounds invalid-token probing.
type FargateBundleHandler struct {
	store   *db.Store
	appsDir string
	// tokenKey is the 32-byte HKDF-derived key used to verify bundle tokens.
	// Callers derive it once with deriveBundleTokenKey(authSecret) and pass it here.
	tokenKey []byte
	rl       *keyedRateLimiter
}

// NewFargateBundleHandler constructs a handler with production rate-limit
// settings (10 requests per minute per source IP).
func NewFargateBundleHandler(store *db.Store, appsDir string, tokenKey []byte) *FargateBundleHandler {
	return newFargateBundleHandlerWithRL(store, appsDir, tokenKey, 10, time.Minute)
}

// newFargateBundleHandlerWithRL constructs a handler with configurable rate
// limits. Used by tests to set a tight limit.
func newFargateBundleHandlerWithRL(store *db.Store, appsDir string, tokenKey []byte, limit int, window time.Duration) *FargateBundleHandler {
	return &FargateBundleHandler{
		store:    store,
		appsDir:  appsDir,
		tokenKey: tokenKey,
		rl:       newKeyedRateLimiter(limit, window),
	}
}

// Handle verifies the bearer token, then delegates to serveBundleByDigest.
func (h *FargateBundleHandler) Handle(w http.ResponseWriter, r *http.Request) {
	src := sourceHost(r)
	if !h.rl.allow(src) {
		writeError(w, http.StatusTooManyRequests, "rate limited")
		return
	}
	digest := chi.URLParam(r, "digest")
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if bearer == "" {
		writeError(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	if err := bundletoken.Verify(h.tokenKey, digest, bearer, time.Now().Unix()); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid or expired token")
		return
	}
	serveBundleByDigest(w, r, h.store, h.appsDir, digest)
}
