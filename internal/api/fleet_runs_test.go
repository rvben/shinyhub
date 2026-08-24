package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

func TestFleetRunLifecycleReportsTerminalAndAbandonedState(t *testing.T) {
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "developer", "developer")
	router := srv.Router()
	const runID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	register := authedRequest(t, http.MethodPost, "/api/fleet/runs", []byte(`{"run_id":"`+runID+`","fleet_id":"prod","kind":"fleet_apply","provenance":{}}`), token)
	regRec := httptest.NewRecorder()
	router.ServeHTTP(regRec, register)
	if regRec.Code != http.StatusCreated {
		t.Fatalf("register=%d body=%s", regRec.Code, regRec.Body.String())
	}
	if _, err := store.DB().Exec(`UPDATE fleet_runs SET heartbeat_at = ? WHERE id = ?`, time.Now().Add(-3*time.Minute), runID); err != nil {
		t.Fatal(err)
	}
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, authedRequest(t, http.MethodGet, "/api/fleet/runs/"+runID, nil, token))
	var view struct {
		Run struct {
			Status         string `json:"status"`
			ObservedStatus string `json:"observed_status"`
		} `json:"run"`
	}
	if getRec.Code != http.StatusOK {
		t.Fatalf("get=%d body=%s", getRec.Code, getRec.Body.String())
	}
	if err := json.NewDecoder(getRec.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.Run.Status != "running" || view.Run.ObservedStatus != "abandoned" {
		t.Fatalf("run view = %+v", view.Run)
	}

	finish := authedRequest(t, http.MethodPatch, "/api/fleet/runs/"+runID,
		[]byte(`{"status":"partial","exit_code":4,"exit_reason":"one app failed"}`), token)
	finishRec := httptest.NewRecorder()
	router.ServeHTTP(finishRec, finish)
	if finishRec.Code != http.StatusNoContent {
		t.Fatalf("finish=%d body=%s", finishRec.Code, finishRec.Body.String())
	}
	run, err := store.GetFleetRun(runID)
	if err != nil || run.Status != "partial" || run.ExitCode == nil || *run.ExitCode != 4 || run.FinishedAt == nil {
		t.Fatalf("finished run = %+v err=%v", run, err)
	}
}
