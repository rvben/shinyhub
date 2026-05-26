package worker

import (
	"crypto/tls"
	"net/http"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func TestMTLSDialer_BuildsServerNameAndBaseURL(t *testing.T) {
	d := newMTLSDialer(tls.Certificate{}, nil) // cert + CA pool; nil pool ok for shape test
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
}
