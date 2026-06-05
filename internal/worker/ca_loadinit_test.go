package worker

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// fakeCAStore is an in-memory CAStore with race-safe single-row semantics.
type fakeCAStore struct {
	mu   sync.Mutex
	cert []byte
	enc  []byte
	set  bool
}

func (f *fakeCAStore) GetWorkerCA() ([]byte, []byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.set {
		return nil, nil, false, nil
	}
	return f.cert, f.enc, true, nil
}
func (f *fakeCAStore) PutWorkerCAIfAbsent(cert, enc []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.set {
		return false, nil
	}
	f.cert, f.enc, f.set = cert, enc, true
	return true, nil
}

const testSecret = "phase2-test-secret"

func TestLoadOrInitCA_GeneratesThenLoads(t *testing.T) {
	st := &fakeCAStore{}
	ca1, err := LoadOrInitCA(st, t.TempDir(), testSecret, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Second call (row now present) loads the same CA.
	ca2, err := LoadOrInitCA(st, t.TempDir(), testSecret, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ca1.CertPEM(), ca2.CertPEM()) {
		t.Fatal("second load returned a different CA")
	}
}

func TestLoadOrInitCA_WrongSecretFailsLoudly(t *testing.T) {
	st := &fakeCAStore{}
	if _, err := LoadOrInitCA(st, t.TempDir(), testSecret, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrInitCA(st, t.TempDir(), "different-secret", nil); err == nil {
		t.Fatal("decrypt with wrong secret must fail, not regenerate")
	}
}

func TestLoadOrInitCA_ImportsDiskCA(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := generateCA()
	writeDiskCA(t, dir, certPEM, keyPEM) // helper: write ca-cert.pem/ca-key.pem
	st := &fakeCAStore{}
	ca, err := LoadOrInitCA(st, dir, testSecret, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ca.CertPEM(), certPEM) {
		t.Fatal("imported CA cert differs from disk cert (workers would be orphaned)")
	}
}

func TestLoadOrInitCA_DiskMismatchErrors(t *testing.T) {
	// DB has CA A; disk has a different CA B -> loud error.
	st := &fakeCAStore{}
	if _, err := LoadOrInitCA(st, t.TempDir(), testSecret, nil); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	otherCert, otherKey := generateCA()
	writeDiskCA(t, dir, otherCert, otherKey)
	if _, err := LoadOrInitCA(st, dir, testSecret, nil); err == nil {
		t.Fatal("disk CA differing from DB CA must be a loud error")
	}
}

func TestLoadOrInitCA_ConcurrentConverge(t *testing.T) {
	st := &fakeCAStore{}
	var wg sync.WaitGroup
	results := make([]*CA, 8)
	errs := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = LoadOrInitCA(st, t.TempDir(), testSecret, nil)
		}(i)
	}
	wg.Wait()
	var want []byte
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if want == nil {
			want = results[i].CertPEM()
		} else if !bytes.Equal(results[i].CertPEM(), want) {
			t.Fatal("instances converged on different CAs")
		}
	}
}

// writeDiskCA writes ca-cert.pem and ca-key.pem into dir with mode 0o600.
func writeDiskCA(t *testing.T, dir string, certPEM, keyPEM []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "ca-cert.pem"), certPEM, 0o600); err != nil {
		t.Fatalf("write disk ca cert: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca-key.pem"), keyPEM, 0o600); err != nil {
		t.Fatalf("write disk ca key: %v", err)
	}
}

// Compile-time assertion that fakeCAStore satisfies CAStore.
var _ CAStore = (*fakeCAStore)(nil)
