package worker

import (
	"crypto/tls"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

func TestMTLSDialer_BuildsServerNameAndBaseURL(t *testing.T) {
	mint := func() (tls.Certificate, error) {
		return selfSignedCert(t, time.Now().Add(-time.Minute), time.Now().Add(time.Hour)), nil
	}
	d, err := NewMTLSDialer(mint, nil) // minter + CA pool; nil pool ok for shape test
	if err != nil {
		t.Fatalf("NewMTLSDialer: %v", err)
	}
	w := db.Worker{NodeID: "node-a", AdvertiseAddr: "10.0.0.5:8443"}

	client, base, err := d.DialWorker(w)
	if err != nil {
		t.Fatalf("DialWorker: %v", err)
	}
	if base != "https://10.0.0.5:8443" {
		t.Errorf("base = %q, want https://10.0.0.5:8443", base)
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if sn := tr.TLSClientConfig.ServerName; sn != "node-a.node.shinyhub.internal" {
		t.Errorf("ServerName = %q, want node-a.node.shinyhub.internal", sn)
	}
	// HTTP/1.1 only so WebSocket upgrades and NDJSON streaming work.
	if !strings.Contains(strings.Join(tr.TLSClientConfig.NextProtos, ","), "http/1.1") {
		t.Errorf("NextProtos = %v, want http/1.1", tr.TLSClientConfig.NextProtos)
	}
	if tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 = true, want false (HTTP/1.1 only)")
	}
	// An unreachable or hung remote worker must not pin a forwarding goroutine
	// indefinitely: the transport bounds dials and the response-header wait.
	if tr.DialContext == nil {
		t.Error("DialContext is nil; a dial to an unreachable worker would block until the OS TCP timeout")
	}
	if tr.ResponseHeaderTimeout <= 0 {
		t.Errorf("ResponseHeaderTimeout = %v, want > 0", tr.ResponseHeaderTimeout)
	}
	if tr.TLSHandshakeTimeout <= 0 {
		t.Errorf("TLSHandshakeTimeout = %v, want > 0", tr.TLSHandshakeTimeout)
	}
}
