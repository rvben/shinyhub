package api_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/worker"
	workerapi "github.com/rvben/shinyhub/internal/worker/api"
)

// wireContractCSR returns a fresh ECDSA key (PEM) and a CSR (PEM) for it, the
// same shape a worker agent submits on register/renew.
func wireContractCSR(t *testing.T) (keyPEM, csrPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "shinyhub-worker"},
	}, key)
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	csrPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	return keyPEM, csrPEM
}

// TestWorkerAPI_IncarnationWireContract drives the real HTTP handlers (not the
// registry directly) to assert the fencing incarnation actually serializes on
// the wire: HandleRegister's JSON response carries incarnation (1 for a fresh
// node), HandleHeartbeat's response carries the current incarnation, and a
// heartbeat reporting a stale incarnation (because the control plane reaped
// the worker and bumped it in between) comes back fenced=true with the bumped
// value - the exact signal a worker agent relies on to self-fence.
func TestWorkerAPI_IncarnationWireContract(t *testing.T) {
	store := dbtest.New(t)
	ca, err := worker.OpenCA(t.TempDir(), []string{"good-token"})
	if err != nil {
		t.Fatalf("open ca: %v", err)
	}
	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	wapi := api.NewWorkerAPI(store, reg, ca, "")

	keyPEM, csrPEM := wireContractCSR(t)

	// --- Register over HTTP: assert the response carries incarnation: 1. ---
	regBody, _ := json.Marshal(workerapi.RegisterRequest{
		Token: "good-token", Tier: "burst", AdvertiseAddr: "203.0.113.5:9000", CSRPEM: string(csrPEM),
	})
	regReq := httptest.NewRequest(http.MethodPost, "/api/workers/register", bytes.NewReader(regBody))
	regReq.RemoteAddr = "203.0.113.9:1"
	regW := httptest.NewRecorder()
	wapi.HandleRegister(regW, regReq)
	if regW.Code != http.StatusOK {
		t.Fatalf("register status = %d, want 200: %s", regW.Code, regW.Body.String())
	}
	var regResp workerapi.RegisterResponse
	if err := json.Unmarshal(regW.Body.Bytes(), &regResp); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if regResp.Incarnation != 1 {
		t.Fatalf("register response incarnation = %d, want 1 for a fresh registration", regResp.Incarnation)
	}

	// Build the worker's mTLS client identity from the cert HandleRegister just
	// issued, the same cert a real agent would present on every subsequent call.
	clientCert, err := tls.X509KeyPair([]byte(regResp.CertPEM), keyPEM)
	if err != nil {
		t.Fatalf("build client keypair: %v", err)
	}
	leaf, err := x509.ParseCertificate(clientCert.Certificate[0])
	if err != nil {
		t.Fatalf("parse client cert: %v", err)
	}

	heartbeat := func(reported int64) workerapi.HeartbeatResponse {
		t.Helper()
		hbBody, _ := json.Marshal(workerapi.HeartbeatRequest{Version: "v1", Incarnation: reported})
		hbReq := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat", bytes.NewReader(hbBody))
		hbReq.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
		hbW := httptest.NewRecorder()
		wapi.HandleHeartbeat(hbW, hbReq)
		if hbW.Code != http.StatusOK {
			t.Fatalf("heartbeat status = %d, want 200: %s", hbW.Code, hbW.Body.String())
		}
		var out workerapi.HeartbeatResponse
		if err := json.Unmarshal(hbW.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode heartbeat response: %v", err)
		}
		return out
	}

	// --- Non-fenced heartbeat: reports the incarnation it was just issued. ---
	upResp := heartbeat(regResp.Incarnation)
	if upResp.Fenced {
		t.Fatalf("fresh worker heartbeat wrongly fenced: %+v", upResp)
	}
	if upResp.Incarnation != 1 {
		t.Fatalf("heartbeat response incarnation = %d, want 1", upResp.Incarnation)
	}

	// The control plane reaps the worker (e.g. a missed-heartbeat sweep),
	// bumping its stored incarnation and reassigning its replicas elsewhere.
	if err := reg.Reap(regResp.NodeID); err != nil {
		t.Fatalf("reap: %v", err)
	}

	// --- Fenced heartbeat: the worker still reports its OLD incarnation (it
	// never learned about the reap), so the control plane must fence it and
	// report the bumped current incarnation. ---
	fencedResp := heartbeat(regResp.Incarnation)
	if !fencedResp.Fenced {
		t.Fatalf("stale-incarnation heartbeat response not fenced: %+v", fencedResp)
	}
	if fencedResp.Incarnation != 2 {
		t.Fatalf("fenced heartbeat response incarnation = %d, want 2 (bumped by reap)", fencedResp.Incarnation)
	}
}
