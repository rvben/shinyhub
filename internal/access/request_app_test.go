package access_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/db"
)

type countedAppStore struct {
	*db.Store
	lookups int
}

func (s *countedAppStore) GetAppBySlug(slug string) (*db.App, error) {
	s.lookups++
	return s.Store.GetAppBySlug(slug)
}

func TestAccessChainSharesAppOnlyWithinAuthorizedRequest(t *testing.T) {
	st := &countedAppStore{Store: makeStore(t)}
	if err := st.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	owner, err := st.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateApp(db.CreateAppParams{Slug: "app", Name: "App", OwnerID: owner.ID, Access: "public"}); err != nil {
		t.Fatal(err)
	}
	app, err := st.Store.GetAppBySlug("app")
	if err != nil {
		t.Fatal(err)
	}
	reached := 0
	handler := access.Middleware(st, "test-secret", nil, st.LookupContextUser)(
		access.NeverDeployedMiddleware(st, "test-secret", nil, st.LookupContextUser, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++; w.WriteHeader(http.StatusNoContent) })))
	serve := func() int {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/app/app/", nil))
		return w.Code
	}
	if got := serve(); got != http.StatusOK || reached != 0 || st.lookups != 1 {
		t.Fatalf("undeployed: status=%d reached=%d lookups=%d", got, reached, st.lookups)
	}
	// The durable deployment row must take effect even with deploy_count still zero.
	if _, err := st.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "v1", BundleDir: "/tmp/v1", Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if got := serve(); got != http.StatusNoContent || reached != 1 || st.lookups != 2 {
		t.Fatalf("deployed: status=%d reached=%d lookups=%d", got, reached, st.lookups)
	}
	// A following request must observe access changes rather than reuse stale app data.
	if err := st.SetAppAccess("app", "private"); err != nil {
		t.Fatal(err)
	}
	if got := serve(); got != http.StatusUnauthorized || reached != 1 || st.lookups != 3 {
		t.Fatalf("private: status=%d reached=%d lookups=%d", got, reached, st.lookups)
	}
}
