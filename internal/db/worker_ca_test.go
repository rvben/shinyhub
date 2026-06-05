package db_test

import (
	"bytes"
	"testing"

	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestWorkerCA_PutGetIfAbsent(t *testing.T) {
	s := dbtest.New(t)

	// Empty table -> found=false, no error.
	if _, _, found, err := s.GetWorkerCA(); err != nil || found {
		t.Fatalf("empty: found=%v err=%v, want found=false err=nil", found, err)
	}

	cert := []byte("CERT-PEM")
	enc := []byte{0x00, 0x01, 0x02, 0xff}
	inserted, err := s.PutWorkerCAIfAbsent(cert, enc)
	if err != nil || !inserted {
		t.Fatalf("first put: inserted=%v err=%v, want true/nil", inserted, err)
	}

	gotCert, gotEnc, found, err := s.GetWorkerCA()
	if err != nil || !found {
		t.Fatalf("get after put: found=%v err=%v", found, err)
	}
	if !bytes.Equal(gotCert, cert) || !bytes.Equal(gotEnc, enc) {
		t.Fatalf("roundtrip mismatch: cert=%q enc=%v", gotCert, gotEnc)
	}

	// Second put is a no-op (singleton already present).
	inserted2, err := s.PutWorkerCAIfAbsent([]byte("OTHER"), []byte{0x09})
	if err != nil || inserted2 {
		t.Fatalf("second put: inserted=%v err=%v, want false/nil", inserted2, err)
	}
	// Original row unchanged.
	gotCert2, _, _, _ := s.GetWorkerCA()
	if !bytes.Equal(gotCert2, cert) {
		t.Fatalf("singleton overwritten: %q", gotCert2)
	}
}
