package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/identity"
	"github.com/rvben/shinyhub/internal/proxy"
)

// chainStore satisfies the unexported access.store interface via structural typing.
// GetAppBySlug returns a fixed app; UserCanAccessApp always grants access.
type chainStore struct {
	app *db.App
}

func (s chainStore) GetAppBySlug(slug string) (*db.App, error) {
	if s.app != nil && s.app.Slug == slug {
		return s.app, nil
	}
	return nil, db.ErrNotFound
}

func (s chainStore) UserCanAccessApp(_ string, _ int64) (bool, error) {
	return true, nil
}

// oneUserGroups is a minimal GroupsSource that returns a fixed group list.
type oneUserGroups struct{ group string }

func (g oneUserGroups) GetUserGroups(_ int64) ([]string, error) {
	return []string{g.group}, nil
}

// startChainBackend returns an httptest.Server that records received headers.
func startChainBackend(t *testing.T) (*httptest.Server, func() http.Header) {
	t.Helper()
	var mu sync.Mutex
	var captured http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		captured = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, func() http.Header {
		mu.Lock()
		defer mu.Unlock()
		return captured
	}
}

// buildChain wires access.Middleware -> proxy.Proxy -> backend.
// It returns the combined handler and a function to fetch captured backend headers.
func buildChain(
	t *testing.T,
	app *db.App,
	secret string,
	lookup auth.UserLookup,
	backendURL string,
) (http.Handler, func() http.Header) {
	t.Helper()

	srv, headers := startChainBackend(t)
	if backendURL == "" {
		backendURL = srv.URL
	}
	_ = srv // already registered in t.Cleanup via startChainBackend; this is now a no-op reference

	prx := proxy.New()
	prx.SetPoolSize(app.Slug, 1)
	prx.SetPoolAppID(app.Slug, app.ID)
	prx.SetPoolIdentityHeaders(app.Slug, true)
	prov := identity.NewProvider(secret, oneUserGroups{"eng"})
	prx.SetIdentityProvider(prov.PayloadFor)
	if err := prx.RegisterReplica(app.Slug, 0, backendURL, nil, 1); err != nil {
		t.Fatalf("RegisterReplica: %v", err)
	}

	st := chainStore{app: app}
	mw := access.Middleware(st, secret, nil, lookup)
	chain := mw(prx)
	return chain, headers
}

// TestIdentityChain_SessionUserReachesBackendVerifiable verifies the full chain:
// access middleware authenticates the session cookie, attaches the user to context,
// and the proxy injects identity headers and a verifiable HS256 token.
func TestIdentityChain_SessionUserReachesBackendVerifiable(t *testing.T) {
	const (
		slug   = "demo"
		secret = "chain-test-secret"
		userID = int64(7)
	)

	app := &db.App{ID: 42, Slug: slug, Access: "private"}

	// The user is admin so access.Middleware's admin-bypass passes without
	// needing a UserCanAccessApp DB row.
	lookup := func(id int64) (*auth.ContextUser, error) {
		return &auth.ContextUser{ID: userID, Username: "ana", Role: "developer"}, nil
	}

	srv, getHeaders := startChainBackend(t)
	chain, _ := buildChain(t, app, secret, lookup, srv.URL)

	tok, err := auth.IssueJWT(userID, "ana", "developer", secret)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}

	req := httptest.NewRequest("GET", "/app/"+slug+"/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
	// For a private app with role="developer", UserCanAccessApp must return true;
	// chainStore always grants it.
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	h := getHeaders()
	if h == nil {
		t.Fatal("backend received no headers (request never reached backend)")
	}

	if got := h.Get(identity.HeaderUser); got != "ana" {
		t.Errorf("X-Shinyhub-User = %q, want %q", got, "ana")
	}
	if got := h.Get(identity.HeaderGroups); got != "eng" {
		t.Errorf("X-Shinyhub-Groups = %q, want %q", got, "eng")
	}

	tokenStr := h.Get(identity.HeaderToken)
	if tokenStr == "" {
		t.Fatal("X-Shinyhub-Identity-Token must be present")
	}

	key := identity.DeriveKey(secret, app.ID)
	var claims identity.TokenClaims
	parsed, parseErr := jwt.ParseWithClaims(
		tokenStr,
		&claims,
		func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return key, nil
		},
		jwt.WithAudience(slug),
		jwt.WithIssuer(identity.Issuer),
		jwt.WithLeeway(30),
	)
	if parseErr != nil {
		t.Fatalf("token parse failed: %v", parseErr)
	}
	if !parsed.Valid {
		t.Fatal("identity token is not valid")
	}
	if claims.Subject != "7" {
		t.Errorf("token Subject = %q, want %q", claims.Subject, "7")
	}
	if claims.Role != "developer" {
		t.Errorf("token Role = %q, want %q", claims.Role, "developer")
	}
}

// TestIdentityChain_DisplayNameReachesBackend proves a user's display name
// reaches the app both as the X-Shinyhub-Name header and the identity token's
// `name` claim, when the session-resolved ContextUser carries a DisplayName.
func TestIdentityChain_DisplayNameReachesBackend(t *testing.T) {
	const (
		slug   = "demo"
		secret = "chain-test-secret"
		userID = int64(7)
	)

	app := &db.App{ID: 42, Slug: slug, Access: "private"}
	lookup := func(id int64) (*auth.ContextUser, error) {
		return &auth.ContextUser{ID: userID, Username: "ana", Role: "developer", DisplayName: "Ana Smith"}, nil
	}

	srv, getHeaders := startChainBackend(t)
	chain, _ := buildChain(t, app, secret, lookup, srv.URL)

	tok, err := auth.IssueJWT(userID, "ana", "developer", secret)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	req := httptest.NewRequest("GET", "/app/"+slug+"/", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	h := getHeaders()
	if h == nil {
		t.Fatal("backend received no headers")
	}
	if got := h.Get(identity.HeaderName); got != "Ana Smith" {
		t.Errorf("X-Shinyhub-Name = %q, want %q", got, "Ana Smith")
	}

	tokenStr := h.Get(identity.HeaderToken)
	if tokenStr == "" {
		t.Fatal("X-Shinyhub-Identity-Token must be present")
	}
	key := identity.DeriveKey(secret, app.ID)
	var claims identity.TokenClaims
	if _, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		return key, nil
	}, jwt.WithAudience(slug), jwt.WithIssuer(identity.Issuer), jwt.WithLeeway(30)); err != nil {
		t.Fatalf("token parse failed: %v", err)
	}
	if claims.Name != "Ana Smith" {
		t.Errorf("token name claim = %q, want %q", claims.Name, "Ana Smith")
	}
}

// TestIdentityChain_PublicAppAuthenticatedAndAnonymous verifies two sub-cases
// for a public app:
// (a) a session cookie results in identity headers reaching the backend;
// (b) no cookie + forged inbound X-Shinyhub-User header results in NO X-Shinyhub-*
// headers at the backend (strip works through the full chain).
func TestIdentityChain_PublicAppAuthenticatedAndAnonymous(t *testing.T) {
	const (
		slug   = "demo"
		secret = "chain-test-secret"
		userID = int64(7)
	)

	app := &db.App{ID: 42, Slug: slug, Access: "public"}
	lookup := func(id int64) (*auth.ContextUser, error) {
		return &auth.ContextUser{ID: userID, Username: "ana", Role: "developer"}, nil
	}

	t.Run("with cookie - headers injected", func(t *testing.T) {
		srv, getHeaders := startChainBackend(t)
		chain, _ := buildChain(t, app, secret, lookup, srv.URL)

		tok, err := auth.IssueJWT(userID, "ana", "developer", secret)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}

		req := httptest.NewRequest("GET", "/app/"+slug+"/", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		h := getHeaders()
		if h == nil {
			t.Fatal("backend received no headers")
		}
		if got := h.Get(identity.HeaderUser); got != "ana" {
			t.Errorf("X-Shinyhub-User = %q, want %q", got, "ana")
		}
	})

	t.Run("no cookie with forged header - all stripped", func(t *testing.T) {
		srv, getHeaders := startChainBackend(t)
		chain, _ := buildChain(t, app, secret, nil, srv.URL)

		req := httptest.NewRequest("GET", "/app/"+slug+"/", nil)
		req.Header.Set("X-Shinyhub-User", "forged")
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for public app, got %d", rec.Code)
		}
		h := getHeaders()
		if h == nil {
			t.Fatal("backend received no headers")
		}
		for k := range h {
			if len(k) >= len(identity.HeaderPrefix) {
				prefix := k
				if len(prefix) > len(identity.HeaderPrefix) {
					prefix = prefix[:len(identity.HeaderPrefix)]
				}
				if http.CanonicalHeaderKey(prefix) == http.CanonicalHeaderKey(identity.HeaderPrefix) {
					t.Errorf("forged/injected X-Shinyhub-* header %q reached the backend", k)
				}
			}
		}
		// Explicit check for the forged header.
		if got := h.Get(identity.HeaderUser); got != "" {
			t.Errorf("X-Shinyhub-User reached backend = %q; must be stripped", got)
		}
	})
}

// TestIdentityChain_ForwardAuthStyleContextUser models the forward-auth pattern
// where the user is placed into the request context before access.Middleware runs
// (ResolveOptionalUser honours the context user and skips cookie parsing). The
// backend must receive identity headers with the correct subject.
func TestIdentityChain_ForwardAuthStyleContextUser(t *testing.T) {
	const (
		slug   = "demo"
		secret = "chain-test-secret"
	)

	app := &db.App{ID: 42, Slug: slug, Access: "private"}

	// The context user is an admin, so the admin-bypass in access.Middleware fires
	// without needing a UserCanAccessApp row.
	srv, getHeaders := startChainBackend(t)
	chain, _ := buildChain(t, app, secret, nil, srv.URL)

	req := httptest.NewRequest("GET", "/app/"+slug+"/", nil)
	// No session cookie - forward-auth has already resolved the user.
	ctxUser := &auth.ContextUser{ID: 9, Username: "fa-user", Role: "admin"}
	req = req.WithContext(auth.WithUser(req.Context(), ctxUser))

	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (forward-auth context user must grant access)", rec.Code)
	}

	h := getHeaders()
	if h == nil {
		t.Fatal("backend received no headers")
	}

	if got := h.Get(identity.HeaderUser); got != "fa-user" {
		t.Errorf("X-Shinyhub-User = %q, want %q", got, "fa-user")
	}

	tokenStr := h.Get(identity.HeaderToken)
	if tokenStr == "" {
		t.Fatal("X-Shinyhub-Identity-Token must be present for forward-auth user")
	}

	key := identity.DeriveKey(secret, app.ID)
	var claims identity.TokenClaims
	parsed, parseErr := jwt.ParseWithClaims(
		tokenStr,
		&claims,
		func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return key, nil
		},
		jwt.WithAudience(slug),
		jwt.WithIssuer(identity.Issuer),
		jwt.WithLeeway(30),
	)
	if parseErr != nil {
		t.Fatalf("token parse failed: %v", parseErr)
	}
	if !parsed.Valid {
		t.Fatal("identity token is not valid")
	}
	if claims.Subject != "9" {
		t.Errorf("token Subject = %q, want %q", claims.Subject, "9")
	}
}
