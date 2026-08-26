package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func TestServiceCredentialLifecycleAndScopedAuthentication(t *testing.T) {
	srv, store := newTestServer(t)
	adminToken, _ := seedUserAndJWT(t, store, "admin", "admin")
	if _, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer"); err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"name":"team analytics","role":"developer","apps":["sales"],"unrestricted":false,"expires_in_days":90}`)
	req := httptest.NewRequest(http.MethodPost, "/api/service-accounts/deployment/credentials", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create=%d body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Token == "" || created.ID == 0 {
		t.Fatalf("created = %+v", created)
	}

	me := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	me.Header.Set("Authorization", "Token "+created.Token)
	meRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(meRec, me)
	if meRec.Code != http.StatusOK {
		t.Fatalf("me=%d body=%s", meRec.Code, meRec.Body.String())
	}
	var session struct {
		User struct {
			Username          string `json:"username"`
			PrincipalType     string `json:"principal_type"`
			ServiceAccountKey string `json:"service_account_key"`
			Role              string `json:"role"`
		} `json:"user"`
		AppScope   []string `json:"app_scope"`
		Credential struct {
			Type, Name string
			ID         int64
		} `json:"credential"`
	}
	if err := json.NewDecoder(meRec.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	if session.User.Username != db.SystemUsernameDeploy || session.User.PrincipalType != "service_account" ||
		session.User.Role != "developer" || session.Credential.Type != "service" ||
		len(session.AppScope) != 1 || session.AppScope[0] != "sales" {
		t.Fatalf("session = %+v", session)
	}

	del := httptest.NewRequest(http.MethodDelete, "/api/service-accounts/deployment/credentials/"+jsonNumber(created.ID), nil)
	del.Header.Set("Authorization", "Bearer "+adminToken)
	delRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(delRec, del)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete=%d body=%s", delRec.Code, delRec.Body.String())
	}
	if _, _, err := store.AuthenticateAPIKey(auth.HashAPIKey(created.Token)); err == nil {
		t.Fatal("revoked token still authenticates")
	}
}

func TestServiceCredentialRejectsConfigurationManagedName(t *testing.T) {
	srv, store := newTestServer(t)
	adminToken, _ := seedUserAndJWT(t, store, "reserved-name-admin", "admin")
	if _, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer"); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"name":"shinyhub_deploy_token","role":"developer","apps":["sales"],"unrestricted":false,"expires_in_days":90}`)
	req := httptest.NewRequest(http.MethodPost, "/api/service-accounts/deployment/credentials", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "reserved for server configuration") {
		t.Fatalf("create reserved name=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSiblingServiceCredentialCannotManageFleetRun(t *testing.T) {
	srv, store := newTestServer(t)
	account, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatal(err)
	}
	credentialIDs := map[string]int64{}
	for name, raw := range map[string]string{"team-a": "shk_team_a_secret", "team-b": "shk_team_b_secret"} {
		id, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{UserID: account.ID, KeyHash: auth.HashAPIKey(raw), Name: name,
			CredentialType: "service", CredentialRole: "developer", AppScope: []string{"sales"}})
		if err != nil {
			t.Fatal(err)
		}
		credentialIDs[name] = id
	}
	register := httptest.NewRequest(http.MethodPost, "/api/fleet/runs", bytes.NewReader([]byte(`{"run_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","fleet_id":"team-a","kind":"fleet_apply","provenance":{}}`)))
	register.Header.Set("Authorization", "Token shk_team_a_secret")
	register.Header.Set("Content-Type", "application/json")
	registerRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(registerRec, register)
	if registerRec.Code != http.StatusCreated {
		t.Fatalf("register=%d %s", registerRec.Code, registerRec.Body.String())
	}
	var registered struct {
		Run struct {
			CredentialID   *int64 `json:"credential_id"`
			CredentialType string `json:"credential_type"`
			CredentialName string `json:"credential_name"`
		} `json:"run"`
	}
	if err := json.NewDecoder(registerRec.Body).Decode(&registered); err != nil {
		t.Fatal(err)
	}
	if registered.Run.CredentialID == nil || *registered.Run.CredentialID != credentialIDs["team-a"] ||
		registered.Run.CredentialType != "service" || registered.Run.CredentialName != "team-a" {
		t.Fatalf("registered credential attribution = %+v", registered.Run)
	}
	events, err := store.ListAuditEvents("fleet_apply_started", 10, 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("fleet audit events = %+v, err=%v", events, err)
	}
	if events[0].CredentialID == nil || *events[0].CredentialID != credentialIDs["team-a"] ||
		events[0].CredentialType != "service" || events[0].CredentialName != "team-a" ||
		events[0].PrincipalType != "service_account" || events[0].ServiceAccountKey != "deployment" {
		t.Fatalf("fleet audit attribution = %+v", events[0])
	}

	get := httptest.NewRequest(http.MethodGet, "/api/fleet/runs/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", nil)
	get.Header.Set("Authorization", "Token shk_team_b_secret")
	getRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(getRec, get)
	if getRec.Code != http.StatusForbidden {
		t.Fatalf("sibling get=%d body=%s", getRec.Code, getRec.Body.String())
	}
}

func TestServiceCredentialAuthorizationUsesCredentialRoleAndScope(t *testing.T) {
	srv, store := newTestServer(t)
	account, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatal(err)
	}
	humanOwner, _ := mkUser(t, store, "human-owner", "developer")
	_, _ = mkUser(t, store, "new-owner", "developer")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "sales", Name: "Sales", OwnerID: humanOwner}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "service-owned", Name: "Service owned", OwnerID: account.ID}); err != nil {
		t.Fatal(err)
	}

	createCredential := func(name, raw, role string, scope []string, unrestricted bool) int64 {
		t.Helper()
		id, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{
			UserID: account.ID, KeyHash: auth.HashAPIKey(raw), Name: name,
			CredentialType: "service", CredentialRole: role, AppScope: scope,
			Unrestricted: unrestricted,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	viewerID := createCredential("viewer", "shk_viewer_scope_secret", "viewer", []string{"sales", "service-owned"}, false)
	developerID := createCredential("developer", "shk_developer_scope_secret", "developer", []string{"sales"}, false)
	createCredential("operator", "shk_operator_scope_secret", "operator", []string{"sales"}, false)
	createCredential("empty", "shk_empty_scope_secret", "developer", nil, false)

	// Scope grants read access even though the app belongs to a human.
	if rec := doToken(t, srv, http.MethodGet, "/api/apps/sales", "shk_viewer_scope_secret", nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer GET scoped human app=%d body=%s", rec.Code, rec.Body.String())
	}
	// Viewer remains read-only even when the shared service principal owns the app.
	if rec := doToken(t, srv, http.MethodPatch, "/api/apps/service-owned/access", "shk_viewer_scope_secret", []byte(`{"access":"shared"}`)); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer PATCH service-owned app=%d body=%s", rec.Code, rec.Body.String())
	}
	// Developer scope is an explicit management grant for a human-owned app.
	if rec := doToken(t, srv, http.MethodPatch, "/api/apps/sales/access", "shk_developer_scope_secret", []byte(`{"access":"shared"}`)); rec.Code != http.StatusOK {
		t.Fatalf("developer PATCH scoped human app=%d body=%s", rec.Code, rec.Body.String())
	}
	// Scope stays a hard ceiling.
	if rec := doToken(t, srv, http.MethodGet, "/api/apps/service-owned", "shk_developer_scope_secret", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("developer GET out-of-scope app=%d body=%s", rec.Code, rec.Body.String())
	}

	// Catalog discovery uses the credential's allowlist as its explicit grant,
	// including for human-owned private apps, and reports management capability
	// from the credential role rather than the shared principal's ownership.
	for _, tc := range []struct {
		raw       string
		wantSlugs map[string]bool
	}{
		{"shk_viewer_scope_secret", map[string]bool{"sales": false, "service-owned": false}},
		{"shk_developer_scope_secret", map[string]bool{"sales": true}},
		{"shk_empty_scope_secret", map[string]bool{}},
	} {
		rec := doToken(t, srv, http.MethodGet, "/api/apps", tc.raw, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list apps=%d body=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			Items []struct {
				Slug      string `json:"slug"`
				CanManage bool   `json:"can_manage"`
			} `json:"items"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Items) != len(tc.wantSlugs) {
			t.Fatalf("list apps for %s = %+v, want %v", tc.raw, body.Items, tc.wantSlugs)
		}
		for _, item := range body.Items {
			wantManage, ok := tc.wantSlugs[item.Slug]
			if !ok || item.CanManage != wantManage {
				t.Fatalf("list item for %s = %+v, want %v", tc.raw, item, tc.wantSlugs)
			}
		}
	}

	// The generic personal-token endpoint cannot revoke a sibling service
	// credential that happens to share the same principal ID.
	if rec := doToken(t, srv, http.MethodDelete, "/api/tokens/"+jsonNumber(developerID), "shk_viewer_scope_secret", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("viewer DELETE sibling credential=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doToken(t, srv, http.MethodGet, "/api/auth/me", "shk_developer_scope_secret", nil); rec.Code != http.StatusOK {
		t.Fatalf("sibling credential was revoked: %d body=%s", rec.Code, rec.Body.String())
	}
	if viewerID == developerID {
		t.Fatal("test credentials unexpectedly share an id")
	}

	// Ownership transfer is platform-level: Developer cannot borrow the shared
	// principal's ownership, while an Operator credential may transfer an app in
	// its own scope.
	if rec := doToken(t, srv, http.MethodPost, "/api/apps/sales/owner", "shk_developer_scope_secret", []byte(`{"username":"new-owner"}`)); rec.Code != http.StatusForbidden {
		t.Fatalf("developer transfer ownership=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doToken(t, srv, http.MethodPost, "/api/apps/sales/owner", "shk_operator_scope_secret", []byte(`{"username":"new-owner"}`)); rec.Code != http.StatusOK {
		t.Fatalf("operator transfer ownership=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestScopedServiceCredentialCannotEscapeThroughGlobalOrRelatedAppSurfaces(t *testing.T) {
	srv, store := newTestServer(t)
	account, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatal(err)
	}
	ownerID, _ := mkUser(t, store, "scope-owner", "developer")
	for _, app := range []struct{ slug, project string }{{"sales", "commercial"}, {"finance", "corporate"}} {
		if _, err := store.CreateApp(db.CreateAppParams{Slug: app.slug, Name: app.slug, ProjectSlug: app.project, OwnerID: ownerID}); err != nil {
			t.Fatal(err)
		}
	}
	sales, err := store.GetAppBySlug("sales")
	if err != nil {
		t.Fatal(err)
	}
	finance, err := store.GetAppBySlug("finance")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.GrantSharedData(sales.ID, finance.ID); err != nil {
		t.Fatal(err)
	}
	create := func(name, raw, role string, unrestricted bool) {
		t.Helper()
		var scope []string
		if !unrestricted {
			scope = []string{"sales"}
		}
		if _, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{UserID: account.ID, KeyHash: auth.HashAPIKey(raw), Name: name,
			CredentialType: "service", CredentialRole: role, AppScope: scope, Unrestricted: unrestricted}); err != nil {
			t.Fatal(err)
		}
	}
	create("scoped-operator", "shk_scoped_operator_secret", "operator", false)
	create("scoped-admin", "shk_scoped_admin_secret", "admin", false)
	create("global-operator", "shk_global_operator_secret", "operator", true)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/projects", `{"slug":"new-project"}`},
		{http.MethodPatch, "/api/projects/corporate", `{"name":"Renamed"}`},
		{http.MethodDelete, "/api/projects/missing", ""},
	} {
		rec := doToken(t, srv, tc.method, tc.path, "shk_scoped_operator_secret", []byte(tc.body))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s scoped project mutation=%d body=%s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
	if rec := doToken(t, srv, http.MethodPost, "/api/projects", "shk_global_operator_secret", []byte(`{"slug":"global-project"}`)); rec.Code != http.StatusCreated {
		t.Fatalf("unrestricted operator create project=%d body=%s", rec.Code, rec.Body.String())
	}

	// Existing out-of-scope mounts are omitted, and mutation paths use the same
	// 404 shape as a nonexistent source before consulting the database.
	if rec := doToken(t, srv, http.MethodGet, "/api/apps/sales/shared-data", "shk_scoped_operator_secret", nil); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "finance") {
		t.Fatalf("scoped shared-data list=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		path := "/api/apps/sales/shared-data"
		var body []byte
		if method == http.MethodPost {
			body = []byte(`{"source_slug":"finance"}`)
		} else {
			path += "/finance"
		}
		rec := doToken(t, srv, method, path, "shk_scoped_operator_secret", body)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s out-of-scope shared data=%d body=%s", method, rec.Code, rec.Body.String())
		}
	}

	if rec := doToken(t, srv, http.MethodGet, "/api/audit", "shk_scoped_admin_secret", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("scoped admin audit=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestServiceCredentialMeReportsEffectiveRole(t *testing.T) {
	srv, store := newTestServer(t)
	account, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"viewer", "developer", "operator", "admin"} {
		t.Run(role, func(t *testing.T) {
			raw := "shk_me_" + role + "_secret"
			if _, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{
				UserID: account.ID, KeyHash: auth.HashAPIKey(raw), Name: "me-" + role,
				CredentialType: "service", CredentialRole: role, Unrestricted: true,
			}); err != nil {
				t.Fatal(err)
			}
			rec := doToken(t, srv, http.MethodGet, "/api/auth/me", raw, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET me=%d body=%s", rec.Code, rec.Body.String())
			}
			var body struct {
				User struct {
					Role string `json:"role"`
				} `json:"user"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.User.Role != role {
				t.Fatalf("role=%q want %q", body.User.Role, role)
			}
		})
	}
}

func jsonNumber(v int64) string { return fmt.Sprintf("%d", v) }
