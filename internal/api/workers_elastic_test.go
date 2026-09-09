package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/worker"
	workerapi "github.com/rvben/shinyhub/internal/worker/api"
)

func TestWorkerElasticAuthorizeLiveBinding(t *testing.T) {
	h := newWorkerTestHandler(t, nil)
	peer, nodeID := certForNode(t, httptest.NewRequest(http.MethodPost, "/", nil), h, "burst")
	ctx := context.Background()
	if err := h.store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "unused", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, err := h.store.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.CreateApp(db.CreateAppParams{Slug: "elastic", Name: "Elastic", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	app, err := h.store.GetApp("elastic")
	if err != nil {
		t.Fatal(err)
	}
	dep, err := h.store.BeginDeployment(app.ID, "one", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.DB().Exec(`UPDATE apps SET worker_isolation='per_session',worker_max_workers=1,status='running' WHERE id=?`, app.ID); err != nil {
		t.Fatal(err)
	}
	ok, epoch, err := h.store.AcquireOwner("controller", time.Minute)
	if err != nil || !ok {
		t.Fatalf("owner: %v %v", ok, err)
	}
	owner := db.ElasticOwner{Instance: "controller", Epoch: epoch}
	reservation, err := h.store.ReserveElasticSession(ctx, owner, app.ID, dep.ID, "client")
	if err != nil {
		t.Fatal(err)
	}
	a := workerapi.ElasticAuthority{ReservationID: reservation.ID, NodeID: nodeID, Instance: owner.Instance, Epoch: epoch, RequestDigest: strings.Repeat("a", 64), Action: "prepare"}
	if err := h.store.BindElasticLaunch(ctx, owner, reservation.ID, nodeID, a.RequestDigest); err != nil {
		t.Fatal(err)
	}
	request := func(command workerapi.ElasticAuthority, authenticated bool, want int) {
		t.Helper()
		body, _ := json.Marshal(command)
		r := httptest.NewRequest(http.MethodPost, "/api/workers/elastic/authorize", bytes.NewReader(body))
		if authenticated {
			r.TLS = peer.TLS
		}
		w := httptest.NewRecorder()
		h.HandleElasticAuthorize(w, r)
		if w.Code != want {
			t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
		}
	}
	request(a, true, 204)
	client := elasticAuthorityMTLSClient(t, h, nodeID)
	if err := client.AuthorizeElastic(ctx, a); err != nil {
		t.Fatalf("mTLS authority: %v", err)
	}
	request(a, false, 401)
	wrong := a
	wrong.NodeID = "other-worker"
	request(wrong, true, 403)
	wrong = a
	wrong.Epoch++
	request(wrong, true, 409)
	wrong = a
	wrong.RequestDigest = strings.Repeat("b", 64)
	request(wrong, true, 409)
	wrong = a
	wrong.Action = "stop"
	request(wrong, true, 409)
	for _, body := range []string{strings.Repeat(" ", 4097), `{} {}`} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		r.TLS = peer.TLS
		w := httptest.NewRecorder()
		h.HandleElasticAuthorize(w, r)
		if w.Code != 400 {
			t.Fatalf("malformed status=%d", w.Code)
		}
	}
	if err := h.store.StopElasticReservation(ctx, owner, reservation.ID); err != nil {
		t.Fatal(err)
	}
	request(a, true, 409)
	a.Action = "stop"
	request(a, true, 204)
	if err := h.registry.Revoke(nodeID); err != nil {
		t.Fatal(err)
	}
	request(a, true, 401)
	if err := client.AuthorizeElastic(ctx, a); err == nil {
		t.Fatal("mTLS authority accepted revoked worker")
	}
}

func elasticAuthorityMTLSClient(t *testing.T, h *WorkerAPI, nodeID string) *worker.Client {
	t.Helper()
	key, csr := newCSR(t)
	certPEM, err := h.ca.SignWorkerCSR(nodeID, csr, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, key)
	if err != nil {
		t.Fatal(err)
	}
	serverCert, err := h.ca.ServerCertificate("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/workers/elastic/authorize", h.HandleElasticAuthorize)
	server := httptest.NewUnstartedServer(mux)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{serverCert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: h.ca.Pool(), MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	ca, err := worker.NewCAHolder(h.ca.CertPEM())
	if err != nil {
		t.Fatal(err)
	}
	client, err := worker.NewClient(server.URL, worker.NewCertHolder(cert), ca)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if tr, ok := client.Transport().(*http.Transport); ok {
			tr.CloseIdleConnections()
		}
	})
	return client
}
