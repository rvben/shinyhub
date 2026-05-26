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
// so a heartbeat cannot impersonate another node.
type HeartbeatRequest struct {
	Version string `json:"version"`
}

// HeartbeatResponse optionally carries a renewed certificate. RenewCSRPEM in the
// request (omitted here for brevity in Plan B's heartbeat) drives renewal; when
// the control plane re-signs, CertPEM is non-empty and the agent swaps it in.
type HeartbeatResponse struct {
	CertPEM string `json:"cert_pem,omitempty"`
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
