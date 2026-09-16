package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func TestAppEntitlementsAuthorizationAndEffectiveSources(t *testing.T) {
	srv, store := newTestServer(t)
	users := map[string]*db.User{}
	tokens := map[string]string{}
	for _, name := range []string{"owner", "manager", "viewer", "outsider"} {
		if err := store.CreateUser(db.CreateUserParams{Username: name, PasswordHash: "h", Role: "viewer"}); err != nil {
			t.Fatal(err)
		}
		u, err := store.GetUserByUsername(name)
		if err != nil {
			t.Fatal(err)
		}
		users[name] = u
		token, err := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
		if err != nil {
			t.Fatal(err)
		}
		tokens[name] = token
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "finance", Name: "Finance", OwnerID: users["owner"].ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "other", Name: "Other", OwnerID: users["outsider"].ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.GrantAppAccessWithRole("finance", users["manager"].ID, "manager"); err != nil {
		t.Fatal(err)
	}
	if err := store.GrantAppAccess("finance", users["viewer"].ID); err != nil {
		t.Fatal(err)
	}
	call := func(user, method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := authedRequest(t, method, path, []byte(body), tokens[user])
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s as %s: %d %s, want %d", method, path, user, w.Code, w.Body.String(), want)
		}
		return w
	}
	base := "/api/apps/finance/entitlements"
	call("viewer", "PUT", base+"/power_user", `{}`, http.StatusForbidden)
	call("outsider", "PUT", base+"/power_user", `{}`, http.StatusNotFound)
	call("manager", "PUT", "/api/apps/other/entitlements/power_user", `{}`, http.StatusNotFound)
	call("owner", "PUT", base+"/power_user", `{"description":"Run advanced reports"}`, http.StatusNoContent)
	// Omitted descriptions preserve metadata; an explicit empty string clears it.
	for _, tc := range []struct{ body, description string }{
		{`{}`, "Run advanced reports"},
		{`{"description":""}`, ""},
		{`{"description":"Updated description"}`, "Updated description"},
	} {
		call("owner", "PUT", base+"/power_user", tc.body, http.StatusNoContent)
		response := call("owner", "GET", base, "", http.StatusOK)
		var list struct {
			Items []db.AppEntitlement `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Items) != 1 || list.Items[0].Description != tc.description {
			t.Fatalf("after define %s: %+v, want description %q", tc.body, list.Items, tc.description)
		}
	}
	call("manager", "POST", base+"/power_user/grants", `{"username":"viewer"}`, http.StatusNoContent)
	call("manager", "POST", base+"/power_user/grants", `{"group":"finance-team"}`, http.StatusNoContent)
	call("manager", "POST", base+"/power_user/grants", `{"username":"viewer","group":"team"}`, http.StatusBadRequest)
	call("manager", "POST", base+"/unknown/grants", `{"username":"viewer"}`, http.StatusNotFound)
	if err := store.ReplaceUserGroups(users["viewer"].ID, []string{"finance-team"}); err != nil {
		t.Fatal(err)
	}
	result := call("owner", "GET", base+"/effective?username=viewer", "", http.StatusOK)
	var effective struct {
		Entitlements []string              `json:"entitlements"`
		Sources      []db.EntitlementGrant `json:"sources"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &effective); err != nil {
		t.Fatal(err)
	}
	if len(effective.Entitlements) != 1 || len(effective.Sources) != 2 {
		t.Fatalf("effective = %+v", effective)
	}
	call("manager", "DELETE", base+"/power_user/grants", `{"username":"viewer"}`, http.StatusNoContent)
	result = call("owner", "GET", base+"/effective?username=viewer", "", http.StatusOK)
	if err := json.Unmarshal(result.Body.Bytes(), &effective); err != nil {
		t.Fatal(err)
	}
	if len(effective.Entitlements) != 1 || len(effective.Sources) != 1 || effective.Sources[0].Source != "group" {
		t.Fatalf("revoke erased inherited source: %+v", effective)
	}
	// A business entitlement never promotes the user to app manager.
	call("viewer", "PUT", base+"/new_permission", `{}`, http.StatusForbidden)
	call("viewer", "GET", base+"/effective?username=owner", "", http.StatusForbidden)
	call("owner", "DELETE", base+"/power_user", "", http.StatusConflict)
	call("owner", "DELETE", base+"/power_user/grants", `{"group":"finance-team"}`, http.StatusNoContent)
	call("owner", "DELETE", base+"/power_user", "", http.StatusNoContent)
}

func TestAppEntitlementsRespectCredentialScope(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, _ := mkUser(t, store, "owner", "developer")
	for _, slug := range []string{"inscope", "outscope"} {
		if _, err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: ownerID}); err != nil {
			t.Fatal(err)
		}
	}
	token := scopedDeployToken(t, srv, store, "admin", []string{"inscope"})
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"PUT", "/api/apps/inscope/entitlements/export", `{}`, http.StatusNoContent},
		{"POST", "/api/apps/inscope/entitlements/export/grants", `{"username":"owner"}`, http.StatusNoContent},
		{"PUT", "/api/apps/outscope/entitlements/export", `{}`, http.StatusNotFound},
		{"POST", "/api/apps/outscope/entitlements/export/grants", `{"username":"owner"}`, http.StatusNotFound},
		{"GET", "/api/apps/outscope/entitlements/effective?username=owner", "", http.StatusNotFound},
		{"GET", "/api/apps/outscope/entitlement-grants", "", http.StatusNotFound},
	} {
		response := doToken(t, srv, tc.method, tc.path, token, []byte(tc.body))
		if response.Code != tc.status {
			t.Errorf("%s %s: %d %s, want %d", tc.method, tc.path, response.Code, response.Body.String(), tc.status)
		}
	}
}
