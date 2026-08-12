// Package deployevent defines the negotiated, line-delimited deployment
// progress protocol shared by the server and CLI.
package deployevent

import "encoding/json"

const (
	// MediaType is requested with Accept and returned as Content-Type when the
	// server streams deployment events. Clients that do not request it continue
	// to receive the original single JSON response.
	MediaType = "application/x-shinyhub-deploy-events+json"

	TypePhase  = "phase"
	TypeResult = "result"
	TypeError  = "error"

	StatusStarted   = "started"
	StatusProgress  = "progress"
	StatusCompleted = "completed"
	StatusWarning   = "warning"
	StatusFailed    = "failed"
)

// Event is one NDJSON record in a deployment stream. Fields are deliberately
// additive and optional so older clients can ignore detail added by newer
// servers while preserving the stable type/phase/status core.
type Event struct {
	Type           string          `json:"type"`
	Phase          string          `json:"phase,omitempty"`
	Status         string          `json:"status,omitempty"`
	Message        string          `json:"message,omitempty"`
	ElapsedSeconds int64           `json:"elapsed_seconds,omitempty"`
	Current        int             `json:"current,omitempty"`
	Total          int             `json:"total,omitempty"`
	Replica        *int            `json:"replica,omitempty"`
	FileCount      int             `json:"file_count,omitempty"`
	Bytes          int64           `json:"bytes,omitempty"`
	Digest         string          `json:"digest,omitempty"`
	StatusCode     int             `json:"status_code,omitempty"`
	FailureKind    string          `json:"failure_kind,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
}

// Phase constructs a phase update.
func Phase(phase, status, message string) Event {
	return Event{Type: TypePhase, Phase: phase, Status: status, Message: message}
}
