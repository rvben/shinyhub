package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/rvben/shinyhub/internal/deployevent"
	"github.com/rvben/shinyhub/internal/deployfail"
)

// deployResponder preserves the original deploy JSON API unless the caller
// explicitly negotiates deployment events. Once a stream begins, failures are
// terminal event records because the HTTP status has necessarily been sent.
type deployResponder struct {
	w      http.ResponseWriter
	stream bool
	mu     sync.Mutex
	enc    *json.Encoder
	failed string
}

func newDeployResponder(w http.ResponseWriter, r *http.Request) *deployResponder {
	return &deployResponder{w: w, stream: strings.Contains(r.Header.Get("Accept"), deployevent.MediaType)}
}

func (d *deployResponder) event(event deployevent.Event) {
	if !d.stream {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if event.Type == deployevent.TypePhase && event.Status == deployevent.StatusFailed {
		d.failed = event.Phase
	}
	if d.enc == nil {
		d.w.Header().Set("Content-Type", deployevent.MediaType)
		d.w.Header().Set("X-Content-Type-Options", "nosniff")
		d.w.WriteHeader(http.StatusOK)
		d.enc = json.NewEncoder(d.w)
	}
	_ = d.enc.Encode(event)
	if flusher, ok := d.w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (d *deployResponder) fail(status int, message string, kind deployfail.Kind, phase string) {
	if !d.stream {
		if kind != "" {
			writeErrorWithKind(d.w, status, message, kind)
		} else {
			writeError(d.w, status, message)
		}
		return
	}
	d.mu.Lock()
	if phase == "deploy" && d.failed != "" {
		phase = d.failed
	}
	d.mu.Unlock()
	d.event(deployevent.Event{
		Type: deployevent.TypeError, Phase: phase, Message: message,
		StatusCode: status, FailureKind: string(kind),
	})
}

func (d *deployResponder) result(value any) {
	if !d.stream {
		writeJSON(d.w, http.StatusOK, value)
		return
	}
	raw, err := json.Marshal(value)
	if err != nil {
		d.fail(http.StatusInternalServerError, "could not encode deployment result", "", "commit")
		return
	}
	d.event(deployevent.Event{Type: deployevent.TypeResult, Result: raw})
}
