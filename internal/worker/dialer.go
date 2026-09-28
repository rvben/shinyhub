package worker

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// certPastHalfLife reports whether a cert spanning [notBefore, notAfter] is at
// or past its half-life as of now, the point at which the control plane re-mints
// its client cert so it never dials a worker with an expired identity.
func certPastHalfLife(notBefore, notAfter, now time.Time) bool {
	halfLife := notBefore.Add(notAfter.Sub(notBefore) / 2)
	return !now.Before(halfLife)
}

// rotatingCert lazily re-mints a certificate once the current one passes its
// half-life, so a long-running process never presents an expired cert. The same
// provider drives both the control plane's outbound client cert
// (GetClientCertificate) and the worker-API listener's inbound server cert
// (GetCertificate). It is safe for concurrent use.
type rotatingCert struct {
	mint func() (tls.Certificate, error)

	mu        sync.Mutex
	cert      tls.Certificate
	notBefore time.Time
	notAfter  time.Time
}

func newRotatingCert(mint func() (tls.Certificate, error)) (*rotatingCert, error) {
	r := &rotatingCert{mint: mint}
	if err := r.refresh(); err != nil {
		return nil, err
	}
	return r, nil
}

// refresh mints a new cert and records its validity window. Caller holds mu.
func (r *rotatingCert) refresh() error {
	c, err := r.mint()
	if err != nil {
		return err
	}
	leaf := c.Leaf
	if leaf == nil {
		leaf, err = x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			return fmt.Errorf("parse minted cert: %w", err)
		}
	}
	r.cert = c
	r.notBefore = leaf.NotBefore
	r.notAfter = leaf.NotAfter
	return nil
}

// current returns the held cert, re-minting when it is past its half-life. If a
// re-mint fails while the current cert is still valid, the current cert is kept
// so a transient signing error does not break an otherwise-working provider;
// only once the held cert has expired does the re-mint error surface.
//
// It returns a copy of the held cert, not a pointer to the provider's field: the
// TLS stack reads the returned certificate for the duration of a handshake, and
// a concurrent re-mint reassigns r.cert. Sharing the field would let that
// reassignment mutate a certificate another in-flight handshake is still reading.
func (r *rotatingCert) current() (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if certPastHalfLife(r.notBefore, r.notAfter, time.Now()) {
		if err := r.refresh(); err != nil {
			if !time.Now().Before(r.notAfter) {
				return nil, err
			}
		}
	}
	held := r.cert
	return &held, nil
}

// getClientCertificate adapts the provider to tls.Config.GetClientCertificate
// for the control plane's outbound dials to workers.
func (r *rotatingCert) getClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	return r.current()
}

// getCertificate adapts the provider to tls.Config.GetCertificate for the
// worker-API listener's inbound server cert.
func (r *rotatingCert) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return r.current()
}

// mtlsDialer builds per-worker HTTP/1.1 mTLS clients for the control plane.
type mtlsDialer struct {
	clientCert *rotatingCert
	caPool     *x509.CertPool

	mu    sync.Mutex
	cache map[string]cachedTransport // keyed by worker node id; one live entry per worker
	now   func() time.Time
}

// transportCacheIdleTTL is how long a cached worker transport may go unused
// before a later cache miss evicts it. Workers that leave the fleet are never
// announced to the dialer, so idleness is the only signal that an entry is
// dead; without it the cache would grow with every node ID ever dialed. It is
// well above IdleConnTimeout, so an evicted transport holds no idle
// connections worth keeping, and an active worker is never evicted.
const transportCacheIdleTTL = 10 * time.Minute

// transportKey identifies the worker a cached transport was built for. A
// transport stays valid for as long as the worker's advertise address and the
// dialer's CA pool stay the same: the client certificate rotates through
// GetClientCertificate without needing a new transport, but a different
// address or CA pool means the cached transport no longer belongs to the same
// peer.
type transportKey struct {
	nodeID string
	addr   string
	ca     *x509.CertPool
}

// cachedTransport pairs a built transport with the key it was built for, so a
// lookup can detect that the worker's identity moved on without a second map
// lookup.
type cachedTransport struct {
	key      transportKey
	tr       *http.Transport
	lastUsed time.Time
}

// NewMTLSDialer constructs the default control-plane-to-worker dialer. mintClient
// issues the control plane's short-lived client certificate; the dialer re-mints
// it past its half-life so it is never presented expired. It returns the
// AgentDialer interface because callers wire it into a remoteRuntime, which
// depends only on the interface.
func NewMTLSDialer(mintClient func() (tls.Certificate, error), caPool *x509.CertPool) (AgentDialer, error) {
	rc, err := newRotatingCert(mintClient)
	if err != nil {
		return nil, err
	}
	return &mtlsDialer{clientCert: rc, caPool: caPool, cache: make(map[string]cachedTransport), now: time.Now}, nil
}

func (d *mtlsDialer) tlsConfig(w db.Worker) *tls.Config {
	return &tls.Config{
		GetClientCertificate: d.clientCert.getClientCertificate,
		RootCAs:              d.caPool,
		// ServerName matches the DNS SAN in the worker's issued cert:
		// <nodeid>.node.shinyhub.internal
		ServerName: w.NodeID + nodeIDSANSuffix,
		NextProtos: []string{"http/1.1"},
		MinVersion: tls.VersionTLS12,
	}
}

// workerResponseHeaderTimeout bounds the wait for a remote worker's response
// headers. It bounds only the header wait, so WebSocket upgrades and NDJSON
// streaming (whose headers arrive immediately) are unaffected.
const workerResponseHeaderTimeout = 120 * time.Second

func (d *mtlsDialer) transport(w db.Worker) *http.Transport {
	return &http.Transport{
		TLSClientConfig:   d.tlsConfig(w),
		ForceAttemptHTTP2: false,
		// Bound dials and the response-header wait so an unreachable or hung
		// remote worker cannot pin a forwarding goroutine indefinitely.
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: workerResponseHeaderTimeout,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   16,
	}
}

// transportFor returns the cached *http.Transport for w, building and caching
// one on a cache miss. It is safe to share the returned transport across every
// call for the same worker identity: its connection pool and TLS session cache
// are only useful if kept alive between calls, and the client certificate
// rotates through GetClientCertificate without needing a new transport. When
// the worker's advertise address or the dialer's CA pool has changed since the
// cached entry was built, the stale transport is retired (its idle connections
// closed, since nothing will address them again) and a fresh one replaces it.
// A cache miss also evicts every other entry unused for transportCacheIdleTTL,
// so the cache tracks the workers currently being dialed rather than every
// worker ever seen.
func (d *mtlsDialer) transportFor(w db.Worker) *http.Transport {
	key := transportKey{nodeID: w.NodeID, addr: w.AdvertiseAddr, ca: d.caPool}
	now := d.now()

	d.mu.Lock()
	if entry, ok := d.cache[w.NodeID]; ok && entry.key == key {
		entry.lastUsed = now
		d.cache[w.NodeID] = entry
		d.mu.Unlock()
		return entry.tr
	}
	var retired []*http.Transport
	if stale, ok := d.cache[w.NodeID]; ok {
		retired = append(retired, stale.tr)
	}
	for id, entry := range d.cache {
		if id != w.NodeID && now.Sub(entry.lastUsed) >= transportCacheIdleTTL {
			retired = append(retired, entry.tr)
			delete(d.cache, id)
		}
	}
	tr := d.transport(w)
	d.cache[w.NodeID] = cachedTransport{key: key, tr: tr, lastUsed: now}
	d.mu.Unlock()

	for _, old := range retired {
		old.CloseIdleConnections()
	}
	return tr
}

// forget drops the cached transport for nodeID, closing its idle connections,
// so a worker that can no longer be dialed does not keep one alive.
func (d *mtlsDialer) forget(nodeID string) {
	d.mu.Lock()
	entry, ok := d.cache[nodeID]
	delete(d.cache, nodeID)
	d.mu.Unlock()
	if ok {
		entry.tr.CloseIdleConnections()
	}
}

func (d *mtlsDialer) DialWorker(w db.Worker) (*http.Client, string, error) {
	if w.Revoked() {
		d.forget(w.NodeID)
		return nil, "", fmt.Errorf("worker %q is revoked", w.NodeID)
	}
	if w.AdvertiseAddr == "" {
		return nil, "", fmt.Errorf("worker %q has no advertise address", w.NodeID)
	}
	client := &http.Client{Transport: d.transportFor(w)}
	return client, "https://" + w.AdvertiseAddr, nil
}

func (d *mtlsDialer) Transport(w db.Worker) (http.RoundTripper, error) {
	if w.Revoked() {
		return nil, fmt.Errorf("worker %q is revoked", w.NodeID)
	}
	return d.transport(w), nil
}

var _ AgentDialer = (*mtlsDialer)(nil)
