package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// restrictedEmptyScopeToken registers a pre-shared deploy token whose identity
// is explicitly scope-restricted to NO apps (AppScopeRestricted true, AppScope
// nil), the state auth.ContextUser.HasAppScopeRestriction documents as "no
// apps", not "unrestricted". scopedDeployToken cannot express this: it never
// sets AppScopeRestricted, so it is only usable for a non-empty allowlist or
// the legacy always-unrestricted empty case.
//
// Server.keyLookup stamps every deploy-token identity as a service account
// regardless of the role passed here, so a request authenticated this way
// always exercises handleListApps' IsServiceAccount()/isPrivilegedAppOperator()
// branch, never the plain-visibility branch: see
// restrictedEmptyScopeVisibilityRequest for an identity that reaches that one.
func restrictedEmptyScopeToken(t *testing.T, srv interface {
	SetDeployToken(*auth.DeployToken)
}, store *db.Store, role string) string {
	t.Helper()
	sysUser, err := store.UpsertSystemUser(db.SystemUsernameDeploy, role)
	if err != nil {
		t.Fatalf("upsert system user: %v", err)
	}
	raw := "shk_" + strings.Repeat("d", 64)
	srv.SetDeployToken(auth.NewDeployToken(raw, &auth.ContextUser{
		ID: sysUser.ID, Username: sysUser.Username, Role: role,
		AppScope: nil, AppScopeRestricted: true,
	}))
	return raw
}

// restrictedEmptyScopeVisibilityRequest builds a GET /api/apps request for an
// identity that is scope-restricted to no apps but is neither a service
// account nor a privileged role, so handleListApps must route it through
// ListAppsVisibleToUserInSlugs rather than ListAppsInSlugs. In production, an
// AppScope only ever reaches a ContextUser through a service-account
// credential (see Server.keyLookup), so no token-issuing path produces this
// combination; the identity is attached directly to the request context
// instead, the same seam BearerMiddleware documents for an upstream
// authenticator (forward-auth) that has already resolved the caller.
func restrictedEmptyScopeVisibilityRequest(userID int64, username, role string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
	u := &auth.ContextUser{
		ID: userID, Username: username, Role: role,
		AppScope: nil, AppScopeRestricted: true,
	}
	return req.WithContext(auth.WithUser(req.Context(), u))
}

type appListEnvelope struct {
	Items []struct {
		Slug string `json:"slug"`
	} `json:"items"`
	Total int `json:"total"`
}

// TestListApps_ScopedIdentity_EmptyRestrictedAllowlist_ReturnsEmpty pins the
// case an intersect-then-filter implementation gets wrong for free but an
// SQL-pushed allowlist can get wrong by omission: an identity whose scope is
// intentionally restricted to no apps must see an empty page (items: [],
// total: 0), never the whole fleet or any subset of it, regardless of which
// unscoped listing the identity's role and account kind would otherwise use
// (ListAppsInSlugs for a service account or privileged role, or
// ListAppsVisibleToUserInSlugs for anyone else). A handler that used
// len(u.AppScope) > 0 to decide whether to apply the allowlist, instead of
// u.HasAppScopeRestriction(), would fall through to the matching unscoped
// listing here and leak apps the caller should not be able to see at all.
func TestListApps_ScopedIdentity_EmptyRestrictedAllowlist_ReturnsEmpty(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, _ := mkUser(t, store, "owner", "developer")
	for _, slug := range []string{"fleet-a", "fleet-b", "fleet-c"} {
		if _, err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: ownerID, Access: "private"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetAppAccess("fleet-a", "public"); err != nil {
		t.Fatal(err)
	}

	t.Run("service account, privileged role (whole-fleet branch)", func(t *testing.T) {
		tok := restrictedEmptyScopeToken(t, srv, store, "admin")
		rec := doToken(t, srv, "GET", "/api/apps", tok, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/apps = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var env appListEnvelope
		if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if env.Total != 0 || len(env.Items) != 0 {
			t.Fatalf("GET /api/apps for an empty-restricted admin scope = %+v, want total 0 and no items (leaked the whole fleet)", env)
		}
	})

	t.Run("service account, non-privileged role (still the whole-fleet branch)", func(t *testing.T) {
		// keyLookup marks every deploy-token identity a service account
		// regardless of role, so this identity still goes through
		// ListAppsInSlugs, not the visibility path: it proves role alone does
		// not decide the branch for a deploy-token credential.
		tok := restrictedEmptyScopeToken(t, srv, store, "developer")
		rec := doToken(t, srv, "GET", "/api/apps", tok, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/apps = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var env appListEnvelope
		if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if env.Total != 0 || len(env.Items) != 0 {
			t.Fatalf("GET /api/apps for an empty-restricted service-account developer scope = %+v, want total 0 and no items (leaked the whole fleet)", env)
		}
	})

	t.Run("non-service-account, non-privileged role (visibility branch)", func(t *testing.T) {
		callerID, _ := mkUser(t, store, "caller", "developer")
		req := restrictedEmptyScopeVisibilityRequest(callerID, "caller", "developer")
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/apps = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var env appListEnvelope
		if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if env.Total != 0 || len(env.Items) != 0 {
			t.Fatalf("GET /api/apps for an empty-restricted developer scope = %+v, want total 0 and no items (leaked visible apps despite empty allowlist)", env)
		}
	})
}
