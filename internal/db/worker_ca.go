package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// workerCARole is the constant primary key for the single cp_worker_ca row.
const workerCARole = "worker-ca"

// GetWorkerCA returns the stored CA certificate (PEM) and the encrypted CA
// private key blob. found is false (err nil) when no CA has been stored yet.
// The bytes are opaque to this package; encryption/decryption lives in the
// worker package. A found bool (not an ErrNotFound sentinel) keeps the worker
// CAStore contract free of a db sentinel.
func (s *Store) GetWorkerCA() (certPEM []byte, keyEnc []byte, found bool, err error) {
	var cert string
	var enc []byte
	e := s.db.QueryRow(`SELECT cert_pem, key_pem_enc FROM cp_worker_ca WHERE role = ?`, workerCARole).
		Scan(&cert, &enc)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil, false, nil
	}
	if e != nil {
		return nil, nil, false, fmt.Errorf("get worker ca: %w", e)
	}
	return []byte(cert), enc, true, nil
}

// PutWorkerCAIfAbsent stores the CA iff no row exists yet, returning inserted=true
// only when this call created the row. Concurrent first-boot callers race here;
// exactly one inserts and the rest get inserted=false (race-safe init).
func (s *Store) PutWorkerCAIfAbsent(certPEM, keyEnc []byte) (bool, error) {
	res, err := s.db.Exec(
		`INSERT INTO cp_worker_ca (role, cert_pem, key_pem_enc) VALUES (?, ?, ?)
		 ON CONFLICT(role) DO NOTHING`,
		workerCARole, string(certPEM), keyEnc)
	if err != nil {
		return false, fmt.Errorf("put worker ca: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
