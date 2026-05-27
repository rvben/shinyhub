// internal/worker/agent/renewal.go
package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// renewalPhase classifies where a certificate sits in its lifetime. It drives
// both when the agent requests renewal and how loudly it escalates an
// unfulfilled one: the agent renews from the half-life onward, and an
// outstanding renewal becomes critical once the cert is in the final tenth of
// its lifetime, the point past which the worker risks losing its routing
// identity to an expired cert.
type renewalPhase int

const (
	renewalFresh    renewalPhase = iota // before half-life: nothing to do
	renewalDue                          // past half-life: request renewal
	renewalCritical                     // past 90% of lifetime: escalate loudly
)

// criticalLifetimeNum/Den express the fraction of a cert's lifetime past which an
// unfulfilled renewal is critical (9/10).
const (
	criticalLifetimeNum = 9
	criticalLifetimeDen = 10
)

// classifyRenewal places a certificate spanning [notBefore, notAfter] into its
// renewal phase as of now. A non-positive lifetime (or now past expiry) is
// treated as critical so a malformed or already-expired cert escalates rather
// than reads as fresh.
func classifyRenewal(notBefore, notAfter, now time.Time) renewalPhase {
	total := notAfter.Sub(notBefore)
	if total <= 0 {
		return renewalCritical
	}
	elapsed := now.Sub(notBefore)
	switch {
	case elapsed*criticalLifetimeDen >= total*criticalLifetimeNum:
		return renewalCritical
	case elapsed*2 >= total:
		return renewalDue
	default:
		return renewalFresh
	}
}

// shouldRenew reports whether a certificate spanning [notBefore, notAfter] is at
// or past its half-life as of now, the point at which the agent proactively
// renews so a new cert is in hand well before the current one expires.
func shouldRenew(notBefore, notAfter, now time.Time) bool {
	return classifyRenewal(notBefore, notAfter, now) >= renewalDue
}

// renewalLogFor selects how a renewal-relevant heartbeat should be logged. A
// successful swap is recorded at info; a still-pending renewal escalates from
// warn to error as the cert nears expiry; a fresh cert that needs nothing is
// silent (log=false). Keeping the severity decision pure lets the heartbeat path
// stay thin and keeps this contract under test.
func renewalLogFor(phase renewalPhase, renewed bool) (level slog.Level, msg string, log bool) {
	if renewed {
		return slog.LevelInfo, "worker renewed certificate", true
	}
	switch phase {
	case renewalCritical:
		return slog.LevelError, "worker certificate renewal overdue and near expiry", true
	case renewalDue:
		return slog.LevelWarn, "worker certificate renewal pending", true
	default:
		return 0, "", false
	}
}

// currentRenewalPhase classifies the agent's live certificate and returns its
// phase together with the cert's expiry. A missing or unparseable cert reads as
// fresh with a zero expiry so the heartbeat carries no renewal request.
func (a *Agent) currentRenewalPhase(now time.Time) (renewalPhase, time.Time) {
	cur := a.certs.Get()
	if len(cur.Certificate) == 0 {
		return renewalFresh, time.Time{}
	}
	leaf, err := x509.ParseCertificate(cur.Certificate[0])
	if err != nil {
		return renewalFresh, time.Time{}
	}
	return classifyRenewal(leaf.NotBefore, leaf.NotAfter, now), leaf.NotAfter
}

// recordRenewal emits the structured, severity-escalating log for a
// renewal-relevant heartbeat and maintains the consecutive-failure streak that
// accompanies it. A successful swap clears the streak; an unfulfilled request
// past half-life increments it, so the escalating log shows how many heartbeats
// running renewal has been stuck while the cert ticks toward expiry.
func (a *Agent) recordRenewal(ctx context.Context, phase renewalPhase, notAfter time.Time, renewed bool, cause error) {
	switch {
	case renewed:
		a.renewFailures = 0
	case phase >= renewalDue:
		a.renewFailures++
	}
	level, msg, ok := renewalLogFor(phase, renewed)
	if !ok {
		return
	}
	attrs := []any{"node_id", a.nodeID, "not_after", notAfter}
	if !renewed {
		attrs = append(attrs, "consecutive_failures", a.renewFailures)
	}
	if cause != nil {
		attrs = append(attrs, "err", cause)
	}
	slog.Log(ctx, level, msg, attrs...)
}

// applyRenewedCert rebuilds the keypair from the re-signed cert and the retained
// private key, swaps it into the holder so live listeners and clients present it
// on their next handshake, and persists it for restart re-adoption.
func (a *Agent) applyRenewedCert(certPEM string) error {
	newCert, err := tls.X509KeyPair([]byte(certPEM), a.keyPEM)
	if err != nil {
		return fmt.Errorf("load renewed keypair: %w", err)
	}
	a.certs.Set(newCert)
	path := filepath.Join(a.cfg.DataDir, "agent", "client-cert.pem")
	if err := os.WriteFile(path, []byte(certPEM), 0o600); err != nil {
		return fmt.Errorf("persist renewed cert: %w", err)
	}
	return nil
}
