// internal/worker/ca.go
package worker

import (
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

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ca key: %w", err)
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
		return nil, fmt.Errorf("self-sign ca: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal ca key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
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
