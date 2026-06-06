// internal/worker/ca_cross_instance_test.go
package worker_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/worker"
)

// TestCA_CrossInstanceTrust proves a newly-started instance ("instance B"),
// loading the shared worker CA from the same DB, trusts worker certs the prior
// active ("instance A") signed. This pins the Phase 2 property that the worker
// mTLS trust pool needs no rebuild on failover - both instances load the same DB
// CA at boot. Runs on SQLite and, when configured, Postgres via dbtest.
func TestCA_CrossInstanceTrust(t *testing.T) {
	store := dbtest.New(t)
	const secret = "cross-instance-secret"

	caA, err := worker.LoadOrInitCA(store, t.TempDir(), secret, nil)
	if err != nil {
		t.Fatalf("instance A load CA: %v", err)
	}

	// Instance A signs a worker cert.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "ignored"},
	}, key)
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	certPEM, err := caA.SignWorkerCSR("node-x", csrPEM, time.Hour)
	if err != nil {
		t.Fatalf("instance A sign: %v", err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse signed cert: %v", err)
	}

	// Instance B loads the SAME CA from the shared DB (a distinct, empty caDir) and
	// must trust A's cert with no rebuild.
	caB, err := worker.LoadOrInitCA(store, t.TempDir(), secret, nil)
	if err != nil {
		t.Fatalf("instance B load CA: %v", err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     caB.Pool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("instance B does not trust a cert instance A signed: %v", err)
	}
}
