package api_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterFleetRunIsAuthenticatedAndIdempotent(t *testing.T) {
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "developer", "developer")
	body := []byte(`{"run_id":"0123456789abcdef0123456789abcdef","fleet_id":"prod-eu","kind":"fleet_apply","provenance":{"provider":"gitlab","source":{"label":"Pipeline #42","url":"https://gitlab.example/pipelines/42"}}}`)
	request := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/fleet/runs", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		srv.Router().ServeHTTP(rr, req)
		return rr
	}
	if rr := request(); rr.Code != http.StatusCreated {
		t.Fatalf("first status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := request(); rr.Code != http.StatusOK {
		t.Fatalf("repeat status=%d body=%s", rr.Code, rr.Body.String())
	}
	run, err := store.GetFleetRun("0123456789abcdef0123456789abcdef")
	if err != nil || run.Provenance.Provider != "gitlab" {
		t.Fatalf("stored run=%#v err=%v", run, err)
	}
}
