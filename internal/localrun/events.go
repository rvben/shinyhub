package localrun

import (
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Event is a local development lifecycle or log record. Attempt identifies a
// candidate; generation advances only after public readiness succeeds.
// Observers must accept concurrent calls; the runner serializes them per app.
type Event struct {
	PID        int       `json:"pid,omitempty"`
	Type       string    `json:"type"`
	App        string    `json:"app"`
	At         time.Time `json:"at"`
	Phase      string    `json:"phase,omitempty"`
	Attempt    int64     `json:"attempt,omitempty"`
	Generation int64     `json:"generation,omitempty"`
	URL        string    `json:"url,omitempty"`
	Source     string    `json:"source,omitempty"`
	Stream     string    `json:"stream,omitempty"`
	Message    string    `json:"message,omitempty"`
}

type runEvents struct {
	app      string
	callback func(Event)
	mu       sync.Mutex
	attempt  atomic.Int64
}

func (e *runEvents) emit(event Event) {
	if e == nil || e.callback == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	event.App = e.app
	event.At = time.Now().UTC()
	e.callback(event)
}
func (e *runEvents) phase(phase, message string) {
	e.emit(Event{Type: "phase", Phase: phase, Attempt: e.attempt.Load(), Message: message})
}
func (e *runEvents) writer(w io.Writer, source, stream string, attempt int64) io.Writer {
	if e.callback == nil {
		return w
	}
	return &eventLogWriter{out: w, events: e, source: source, stream: stream, attempt: attempt}
}

// A bounded fragment buffer handles subprocess writes that split lines or never
// emit a newline. Terminal control bytes remain data, never UI instructions.
type eventLogWriter struct {
	mu             sync.Mutex
	out            io.Writer
	events         *runEvents
	source, stream string
	attempt        int64 // 0 follows the current lifecycle attempt (runner diagnostics)
	fragment       string
}

func (w *eventLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.out.Write(p)
	w.fragment += string(p[:n])
	for len(w.fragment) > 0 {
		at := strings.IndexByte(w.fragment, '\n')
		if at < 0 && len(w.fragment) < 8192 {
			break
		}
		size := at
		if size < 0 || size > 8192 {
			size = 8192
		}
		line := w.fragment[:size]
		w.fragment = w.fragment[size:]
		if strings.HasPrefix(w.fragment, "\n") {
			w.fragment = w.fragment[1:]
		}
		attempt := w.attempt
		if attempt == 0 {
			attempt = w.events.attempt.Load()
		}
		w.events.emit(Event{Type: "log", Source: w.source, Stream: w.stream, Attempt: attempt, Message: strings.TrimSuffix(line, "\r")})
	}
	return n, err
}
func (w *eventLogWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fragment == "" {
		return
	}
	attempt := w.attempt
	if attempt == 0 {
		attempt = w.events.attempt.Load()
	}
	w.events.emit(Event{Type: "log", Source: w.source, Stream: w.stream, Attempt: attempt, Message: w.fragment})
	w.fragment = ""
}
func flushEventWriter(w io.Writer) {
	if w, ok := w.(*eventLogWriter); ok {
		w.flush()
	}
}
