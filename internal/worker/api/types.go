// internal/worker/api/types.go

// Package api defines the JSON request/response types and the NDJSON streaming
// framing that form the wire contract between the ShinyHub control plane and a
// worker agent. Both sides import this package so the contract cannot drift.
package api

// RegisterRequest is the join payload a worker presents to
// POST /api/workers/register. Token is the pre-shared join token (the only
// unauthenticated field; the rest of the API authenticates by client cert).
// CSRPEM is the worker's PEM-encoded certificate signing request.
type RegisterRequest struct {
	Token         string `json:"token"`
	Name          string `json:"name"`
	AdvertiseAddr string `json:"advertise_addr"`
	Tier          string `json:"tier"`
	Version       string `json:"version"`
	CSRPEM        string `json:"csr_pem"`
}

// RegisterResponse returns the assigned node id, the signed client certificate,
// and the CA bundle the worker pins for the control-plane server cert.
type RegisterResponse struct {
	NodeID   string `json:"node_id"`
	CertPEM  string `json:"cert_pem"`
	CABundle string `json:"ca_bundle"`
}

// HeartbeatRequest carries the worker's current version. The node id is NOT in
// the body: the control plane derives it from the presented client certificate
// so a heartbeat cannot impersonate another node. RenewCSRPEM, when non-empty,
// is a PEM-encoded CSR the worker submits to renew its certificate before the
// current one expires; the control plane re-signs it and returns the new cert.
type HeartbeatRequest struct {
	Version     string `json:"version"`
	RenewCSRPEM string `json:"renew_csr_pem,omitempty"`
}

// HeartbeatResponse carries a renewed certificate when the request included a
// RenewCSRPEM the control plane re-signed; CertPEM is then non-empty and the
// agent swaps it in. CABundle carries the control plane's current CA bundle so a
// rotated trust root reaches the worker; the agent applies it only when it
// differs from the bundle it already trusts.
type HeartbeatResponse struct {
	CertPEM  string `json:"cert_pem,omitempty"`
	CABundle string `json:"ca_bundle,omitempty"`
}

// FrameKind identifies an NDJSON streaming frame's payload. Used by the replica
// control API to multiplex log output, stat samples, and the terminal result of
// a Start/RunOnce over a single streamed response.
type FrameKind string

const (
	FrameLog    FrameKind = "log"
	FrameStat   FrameKind = "stat"
	FrameResult FrameKind = "result"
	FrameError  FrameKind = "error"
)

// Frame is one NDJSON line in a streamed response. Exactly one of the payload
// fields is meaningful per Kind; Data carries log bytes for FrameLog.
type Frame struct {
	Kind  FrameKind `json:"kind"`
	Data  []byte    `json:"data,omitempty"`
	Error string    `json:"error,omitempty"`
}
