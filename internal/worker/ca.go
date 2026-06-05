// internal/worker/ca.go
package worker

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/rvben/shinyhub/internal/secrets"
)

// nodeIDSANSuffix namespaces the DNS SAN that carries a worker's node id, so the
// id is recoverable from the presented client certificate alone.
const nodeIDSANSuffix = ".node.shinyhub.internal"

// CA is the control plane's internal certificate authority. It signs short-lived
// worker client certificates that bind a node id, and pins itself as the trust
// root both sides verify against. The keypair is generated on first OpenCA and
// persisted under dir.
type CA struct {
	cert       *x509.Certificate
	certPEM    []byte
	key        *ecdsa.PrivateKey
	pool       *x509.CertPool
	joinTokens []string
}

// generateCA creates a fresh self-signed ECDSA P-256 worker CA, returning the
// cert and key as PEM. (Extracted from OpenCA so both disk import and DB init
// share one generator.)
func generateCA() (certPEM, keyPEM []byte) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err) // P-256 keygen from crypto/rand cannot fail in practice
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ShinyHub Worker CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		panic(err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// OpenCA loads the CA keypair from dir, generating and persisting it on first
// run. joinTokens is the set of currently valid join tokens.
func OpenCA(dir string, joinTokens []string) (*CA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("ca dir: %w", err)
	}
	certPath := filepath.Join(dir, "ca-cert.pem")
	keyPath := filepath.Join(dir, "ca-key.pem")

	if certPEM, err := os.ReadFile(certPath); err == nil {
		keyPEM, kerr := os.ReadFile(keyPath)
		if kerr != nil {
			return nil, fmt.Errorf("ca key missing but cert present: %w", kerr)
		}
		return loadCA(certPEM, keyPEM, joinTokens)
	}

	certPEM, keyPEM := generateCA()
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write ca cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write ca key: %w", err)
	}
	return loadCA(certPEM, keyPEM, joinTokens)
}

func loadCA(certPEM, keyPEM []byte, joinTokens []string) (*CA, error) {
	cb, _ := pem.Decode(certPEM)
	if cb == nil {
		return nil, fmt.Errorf("decode ca cert pem")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse ca cert: %w", err)
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, fmt.Errorf("decode ca key pem")
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse ca key: %w", err)
	}
	if !cert.IsCA || !cert.BasicConstraintsValid {
		return nil, fmt.Errorf("ca cert is not a valid CA")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, fmt.Errorf("ca cert lacks cert-sign key usage")
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, fmt.Errorf("ca cert public key does not match private key")
	}
	if err := cert.CheckSignatureFrom(cert); err != nil {
		return nil, fmt.Errorf("ca cert self-signature invalid: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &CA{cert: cert, certPEM: certPEM, key: key, pool: pool, joinTokens: joinTokens}, nil
}

// CertPEM returns the PEM-encoded CA certificate (the bundle workers pin).
func (c *CA) CertPEM() []byte { return c.certPEM }

// Pool returns a verifier pool trusting this CA.
func (c *CA) Pool() *x509.CertPool { return c.pool }

// VerifyJoinToken reports whether token matches a configured join token, using a
// constant-time comparison to avoid leaking token length/contents via timing.
func (c *CA) VerifyJoinToken(token string) bool {
	for _, t := range c.joinTokens {
		if subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
			return true
		}
	}
	return false
}

// SignWorkerCSR signs a worker CSR, binding nodeID into the certificate (CN plus
// a DNS SAN) so the node id is recoverable from the presented cert. The cert is
// valid for ttl (short-lived; renewed on heartbeat).
func (c *CA) SignWorkerCSR(nodeID string, csrPEM []byte, ttl time.Duration) ([]byte, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return nil, fmt.Errorf("decode csr pem")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse csr: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("csr signature: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: nodeID},
		DNSNames:     []string{nodeID + nodeIDSANSuffix},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, csr.PublicKey, c.key)
	if err != nil {
		return nil, fmt.Errorf("sign cert: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// NodeIDFromCert recovers the node id bound into a worker certificate, preferring
// the namespaced DNS SAN and falling back to the CN.
func NodeIDFromCert(cert *x509.Certificate) string {
	for _, name := range cert.DNSNames {
		if len(name) > len(nodeIDSANSuffix) && name[len(name)-len(nodeIDSANSuffix):] == nodeIDSANSuffix {
			return name[:len(name)-len(nodeIDSANSuffix)]
		}
	}
	return cert.Subject.CommonName
}

// Fingerprint returns the hex SHA-256 of a certificate's DER, used to record the
// trusted client cert on the worker row.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// ControlClientCertificate issues a short-lived ECDSA client certificate that
// the control plane presents to worker agents over mTLS. The cert is signed by
// this CA, carries only the ClientAuth EKU, and verifies against CA.Pool().
func (c *CA) ControlClientCertificate() (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate control client key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "shinyhub-control-plane"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &priv.PublicKey, c.key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("sign control client cert: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse control client cert: %w", err)
	}
	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
		Leaf:        leaf,
	}, nil
}

// ServerCertificate signs a fresh server keypair off the CA for the control
// plane's worker-facing listener. When no hosts are given it defaults to
// loopback (127.0.0.1, ::1, localhost). IP-literal hosts become IP SANs and
// the rest become DNS SANs. The returned certificate carries the leaf and the
// CA certificate so clients pinning the CA can build a full chain.
func (c *CA) ServerCertificate(hosts ...string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate server key: %w", err)
	}
	if len(hosts) == 0 {
		hosts = []string{"127.0.0.1", "::1", "localhost"}
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "shinyhub-control-plane"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("sign server cert: %w", err)
	}
	return tls.Certificate{
		Certificate: [][]byte{der, c.cert.Raw},
		PrivateKey:  key,
	}, nil
}

// ListenerTLSConfig builds the TLS config for the control plane's worker-facing
// listener. The server cert is minted off the CA for the given hosts and served
// through GetCertificate, which re-mints it past its half-life so a control
// plane running beyond the cert's lifetime keeps serving without a restart.
// Client certs are requested but not required (register runs before a worker
// has a cert) and verified against the CA when presented.
func (c *CA) ListenerTLSConfig(hosts ...string) (*tls.Config, error) {
	rc, err := newRotatingCert(func() (tls.Certificate, error) {
		return c.ServerCertificate(hosts...)
	})
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: rc.getCertificate,
		ClientAuth:     tls.VerifyClientCertIfGiven,
		ClientCAs:      c.pool,
	}, nil
}

// workerCAKeyInfo domain-separates the CA key derivation from app env secrets.
const workerCAKeyInfo = "shinyhub-worker-ca-v1"

// CAStore is the minimal store the worker CA bootstrap needs. *db.Store
// satisfies it. found (not an ErrNotFound sentinel) signals an empty row.
type CAStore interface {
	GetWorkerCA() (certPEM, keyEnc []byte, found bool, err error)
	PutWorkerCAIfAbsent(certPEM, keyEnc []byte) (inserted bool, err error)
}

// LoadOrInitCA loads the shared worker CA from the store, importing an existing
// on-disk CA from caDir or generating a fresh one on first boot, and converging
// all instances on a single CA via race-safe insert. The private key is
// encrypted at rest with a domain-separated key derived from authSecret. A
// decrypt failure (auth.secret changed) or a disk CA that differs from the DB CA
// is a loud fatal error - never a silent regeneration that would orphan workers.
func LoadOrInitCA(store CAStore, caDir, authSecret string, joinTokens []string) (*CA, error) {
	keyEncKey := secrets.DeriveKeyWithInfo(authSecret, workerCAKeyInfo)

	certPEM, keyEnc, found, err := store.GetWorkerCA()
	if err != nil {
		return nil, fmt.Errorf("load worker ca: %w", err)
	}
	if found {
		return decodeAndGuard(certPEM, keyEnc, keyEncKey, caDir, joinTokens)
	}

	// Init: import an existing disk CA if present, else generate fresh.
	var newCert, newKey []byte
	if dc, dk, ok := readDiskCA(caDir); ok {
		newCert, newKey = dc, dk
	} else {
		newCert, newKey = generateCA()
	}
	enc, err := secrets.Encrypt(keyEncKey, newKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt ca key: %w", err)
	}
	inserted, err := store.PutWorkerCAIfAbsent(newCert, enc)
	if err != nil {
		return nil, fmt.Errorf("store worker ca: %w", err)
	}
	if !inserted {
		// Lost the race: another instance stored a CA first. Adopt it, but if we
		// were importing a disk CA and it differs, fail loudly.
		certPEM, keyEnc, found, err = store.GetWorkerCA()
		if err != nil || !found {
			return nil, fmt.Errorf("worker ca race reread: found=%v err=%w", found, err)
		}
		return decodeAndGuard(certPEM, keyEnc, keyEncKey, caDir, joinTokens)
	}
	return loadCA(newCert, newKey, joinTokens)
}

// decodeAndGuard decrypts the stored key, applies the disk-vs-DB mismatch guard,
// and loads the CA.
func decodeAndGuard(certPEM, keyEnc, keyEncKey []byte, caDir string, joinTokens []string) (*CA, error) {
	keyPEM, err := secrets.Decrypt(keyEncKey, keyEnc)
	if err != nil {
		return nil, fmt.Errorf("worker CA decrypt failed (auth.secret changed?): %w", err)
	}
	if diskCert, ok := readDiskCACert(caDir); ok && !bytes.Equal(diskCert, certPEM) {
		return nil, fmt.Errorf("local disk CA differs from the authoritative DB CA; " +
			"to migrate to HA boot the original CA-owning node first, or to adopt the disk CA " +
			"stop all instances, delete the cp_worker_ca row, and start the CA-owning node first")
	}
	return loadCA(certPEM, keyPEM, joinTokens)
}

// readDiskCA reads ca-cert.pem + ca-key.pem from dir; ok is false if either is absent.
func readDiskCA(dir string) (certPEM, keyPEM []byte, ok bool) {
	c, err := os.ReadFile(filepath.Join(dir, "ca-cert.pem"))
	if err != nil {
		return nil, nil, false
	}
	k, err := os.ReadFile(filepath.Join(dir, "ca-key.pem"))
	if err != nil {
		return nil, nil, false
	}
	return c, k, true
}

// readDiskCACert reads just the disk ca-cert.pem; ok false if absent.
func readDiskCACert(dir string) (certPEM []byte, ok bool) {
	c, err := os.ReadFile(filepath.Join(dir, "ca-cert.pem"))
	if err != nil {
		return nil, false
	}
	return c, true
}
