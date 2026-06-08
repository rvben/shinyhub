package worker

import (
	"crypto/tls"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// stubWorkerStore is a test-only WorkerGetter backed by a map.
type stubWorkerStore struct {
	workers map[string]*db.Worker
}

func newStubWorkerStore(ws ...db.Worker) *stubWorkerStore {
	m := make(map[string]*db.Worker, len(ws))
	for i := range ws {
		w := ws[i]
		m[w.NodeID] = &w
	}
	return &stubWorkerStore{workers: m}
}

func (s *stubWorkerStore) GetWorker(nodeID string) (*db.Worker, error) {
	w, ok := s.workers[nodeID]
	if !ok {
		return nil, db.ErrNotFound
	}
	return w, nil
}

// buildTestDialer returns an AgentDialer whose Transport method returns a
// distinct *http.Transport per worker, seeded from the given CA+caPool so the
// TLS fields match the production path.
func buildTestDialer(t *testing.T, ca *CA) AgentDialer {
	t.Helper()
	mint := func() (tls.Certificate, error) {
		return ca.ControlClientCertificate()
	}
	d, err := NewMTLSDialer(mint, ca.Pool())
	if err != nil {
		t.Fatalf("NewMTLSDialer: %v", err)
	}
	return d
}

// TestReplicaTransportBuilder_Equivalence verifies that TransportForReplica
// produces a transport with the same TLS configuration (ServerName, RootCAs)
// as dialer.Transport called with the same db.Worker - proving the DB path is
// equivalent to the registry path.
func TestReplicaTransportBuilder_Equivalence(t *testing.T) {
	ca, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open CA: %v", err)
	}

	w := db.Worker{
		NodeID:        "node-a",
		Tier:          "remote",
		AdvertiseAddr: "192.0.2.5:8443",
		Status:        "up",
	}
	store := newStubWorkerStore(w)
	dialer := buildTestDialer(t, ca)

	builder := NewReplicaTransportBuilder(dialer, store)

	row := &db.Replica{
		Provider: ProviderRemoteDocker,
		WorkerID: "node-a",
	}

	got, err := builder.TransportForReplica(row)
	if err != nil {
		t.Fatalf("TransportForReplica: %v", err)
	}
	if got == nil {
		t.Fatal("TransportForReplica: got nil, want non-nil transport")
	}

	// Build the reference transport the registry path would produce.
	want, err := dialer.Transport(w)
	if err != nil {
		t.Fatalf("dialer.Transport (reference): %v", err)
	}

	// Both transports must be *http.Transport with matching TLS fields.
	gotT, ok := got.(*http.Transport)
	if !ok {
		t.Fatalf("got transport type = %T, want *http.Transport", got)
	}
	wantT, ok := want.(*http.Transport)
	if !ok {
		t.Fatalf("want transport type = %T, want *http.Transport", want)
	}

	gotCfg := gotT.TLSClientConfig
	wantCfg := wantT.TLSClientConfig

	// ServerName must be "<nodeID>.node.shinyhub.internal" on both paths.
	expectedSN := "node-a" + nodeIDSANSuffix
	if gotCfg.ServerName != expectedSN {
		t.Errorf("got ServerName = %q, want %q", gotCfg.ServerName, expectedSN)
	}
	if wantCfg.ServerName != expectedSN {
		t.Errorf("want ServerName = %q, want %q", wantCfg.ServerName, expectedSN)
	}
	if gotCfg.ServerName != wantCfg.ServerName {
		t.Errorf("ServerName mismatch: got %q, want %q", gotCfg.ServerName, wantCfg.ServerName)
	}

	// Both must share the same RootCAs pool pointer (same CA instance).
	if gotCfg.RootCAs != ca.Pool() {
		t.Error("got transport RootCAs != CA pool")
	}
	if wantCfg.RootCAs != ca.Pool() {
		t.Error("want transport RootCAs != CA pool")
	}

	// MinVersion must be TLS 1.2 on both paths.
	if gotCfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("got MinVersion = %d, want TLS 1.2 (%d)", gotCfg.MinVersion, tls.VersionTLS12)
	}
	if wantCfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("want MinVersion = %d, want TLS 1.2 (%d)", wantCfg.MinVersion, tls.VersionTLS12)
	}

	// NextProtos must include http/1.1 on both paths (WebSocket + NDJSON streaming).
	hasHTTP11 := func(protos []string) bool {
		for _, p := range protos {
			if p == "http/1.1" {
				return true
			}
		}
		return false
	}
	if !hasHTTP11(gotCfg.NextProtos) {
		t.Errorf("got NextProtos = %v, want http/1.1 present", gotCfg.NextProtos)
	}
	if !hasHTTP11(wantCfg.NextProtos) {
		t.Errorf("want NextProtos = %v, want http/1.1 present", wantCfg.NextProtos)
	}
}

// TestReplicaTransportBuilder_NoRegistry verifies that TransportForReplica
// succeeds even when no placement registry exists - the builder uses only the
// DB store and the dialer.
func TestReplicaTransportBuilder_NoRegistry(t *testing.T) {
	ca, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open CA: %v", err)
	}

	w := db.Worker{
		NodeID:        "node-b",
		Tier:          "remote",
		AdvertiseAddr: "192.0.2.6:8443",
		Status:        "up",
	}
	store := newStubWorkerStore(w)
	dialer := buildTestDialer(t, ca)

	// Deliberately do NOT create any registry - the builder must work without one.
	builder := NewReplicaTransportBuilder(dialer, store)

	row := &db.Replica{Provider: ProviderRemoteDocker, WorkerID: "node-b"}
	tr, err := builder.TransportForReplica(row)
	if err != nil {
		t.Fatalf("TransportForReplica with empty registry: %v", err)
	}
	if tr == nil {
		t.Fatal("TransportForReplica: got nil, want mTLS transport")
	}

	// Confirm the transport targets the right worker.
	ht, ok := tr.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", tr)
	}
	wantSN := "node-b" + nodeIDSANSuffix
	if ht.TLSClientConfig.ServerName != wantSN {
		t.Errorf("ServerName = %q, want %q", ht.TLSClientConfig.ServerName, wantSN)
	}
}

// TestReplicaTransportBuilder_Cache verifies that a second call for the same
// worker_id returns the cached transport (same pointer, not rebuilt), and that
// two different worker_ids get distinct transports.
func TestReplicaTransportBuilder_Cache(t *testing.T) {
	ca, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open CA: %v", err)
	}

	store := newStubWorkerStore(
		db.Worker{NodeID: "node-x", Tier: "remote", AdvertiseAddr: "192.0.2.10:8443", Status: "up"},
		db.Worker{NodeID: "node-y", Tier: "remote", AdvertiseAddr: "192.0.2.11:8443", Status: "up"},
	)
	dialer := buildTestDialer(t, ca)
	builder := NewReplicaTransportBuilder(dialer, store)

	rowX := &db.Replica{Provider: ProviderRemoteDocker, WorkerID: "node-x"}
	rowY := &db.Replica{Provider: ProviderRemoteDocker, WorkerID: "node-y"}

	// First call for each worker.
	trX1, err := builder.TransportForReplica(rowX)
	if err != nil {
		t.Fatalf("first call node-x: %v", err)
	}
	trY1, err := builder.TransportForReplica(rowY)
	if err != nil {
		t.Fatalf("first call node-y: %v", err)
	}

	// Second call for the same workers must return the cached instance.
	trX2, err := builder.TransportForReplica(rowX)
	if err != nil {
		t.Fatalf("second call node-x: %v", err)
	}
	trY2, err := builder.TransportForReplica(rowY)
	if err != nil {
		t.Fatalf("second call node-y: %v", err)
	}

	if trX1 != trX2 {
		t.Error("cache miss for node-x: second call returned a different pointer")
	}
	if trY1 != trY2 {
		t.Error("cache miss for node-y: second call returned a different pointer")
	}
	if trX1 == trY1 {
		t.Error("node-x and node-y share the same transport pointer; want distinct")
	}
}

// TestReplicaTransportBuilder_CacheRace exercises concurrent builds of the
// same and different worker transports under the race detector to confirm the
// cache is concurrency-safe.
func TestReplicaTransportBuilder_CacheRace(t *testing.T) {
	ca, err := OpenCA(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open CA: %v", err)
	}

	// All workers share one AdvertiseAddr intentionally: the cache is keyed by
	// NodeID, not address, so address uniqueness is not under test here.
	ws := make([]db.Worker, 5)
	for i := range ws {
		ws[i] = db.Worker{
			NodeID:        "node-race-" + string(rune('a'+i)),
			Tier:          "remote",
			AdvertiseAddr: "192.0.2.20:8443",
			Status:        "up",
		}
	}
	store := newStubWorkerStore(ws...)
	dialer := buildTestDialer(t, ca)
	builder := NewReplicaTransportBuilder(dialer, store)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			row := &db.Replica{
				Provider: ProviderRemoteDocker,
				WorkerID: ws[i%len(ws)].NodeID,
			}
			_, _ = builder.TransportForReplica(row)
		}()
	}
	wg.Wait()
}

// TestReplicaTransportBuilder_Fargate verifies that a fargate-tier replica
// returns nil from TransportForReplica, routing through the default HTTP
// transport rather than the worker mTLS dialer.
func TestReplicaTransportBuilder_Fargate(t *testing.T) {
	mint := func() (tls.Certificate, error) {
		return selfSignedCert(t, time.Now().Add(-time.Minute), time.Now().Add(time.Hour)), nil
	}
	dialer, err := NewMTLSDialer(mint, nil)
	if err != nil {
		t.Fatalf("NewMTLSDialer: %v", err)
	}
	// Store is empty: a fargate row must not trigger a DB lookup.
	store := newStubWorkerStore()
	builder := NewReplicaTransportBuilder(dialer, store)

	fargateRow := &db.Replica{
		Provider: "fargate",
		WorkerID: "fargate", // the synthetic constant used by fargate runtime
	}
	tr, err := builder.TransportForReplica(fargateRow)
	if err != nil {
		t.Fatalf("TransportForReplica fargate: unexpected error: %v", err)
	}
	if tr != nil {
		t.Errorf("TransportForReplica fargate: got %T, want nil (default transport)", tr)
	}

	// Also check ECS EC2 fargate variant.
	ec2Row := &db.Replica{
		Provider: "fargate",
		WorkerID: "ecs-ec2",
	}
	tr2, err := builder.TransportForReplica(ec2Row)
	if err != nil {
		t.Fatalf("TransportForReplica ecs-ec2: unexpected error: %v", err)
	}
	if tr2 != nil {
		t.Errorf("TransportForReplica ecs-ec2: got %T, want nil (default transport)", tr2)
	}
}

// TestReplicaTransportBuilder_UnknownWorker verifies that a remote_docker
// replica referencing a worker_id not in the DB returns an error and no transport.
func TestReplicaTransportBuilder_UnknownWorker(t *testing.T) {
	mint := func() (tls.Certificate, error) {
		return selfSignedCert(t, time.Now().Add(-time.Minute), time.Now().Add(time.Hour)), nil
	}
	dialer, err := NewMTLSDialer(mint, nil)
	if err != nil {
		t.Fatalf("NewMTLSDialer: %v", err)
	}
	store := newStubWorkerStore() // empty
	builder := NewReplicaTransportBuilder(dialer, store)

	row := &db.Replica{Provider: ProviderRemoteDocker, WorkerID: "ghost-node"}
	tr, err := builder.TransportForReplica(row)
	if err == nil {
		t.Error("TransportForReplica for unknown worker: want error, got nil")
	}
	if tr != nil {
		t.Errorf("TransportForReplica for unknown worker: got transport, want nil")
	}
}
