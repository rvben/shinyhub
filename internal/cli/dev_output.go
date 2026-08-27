package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
)

// prefixedWriter keeps concurrent fleet-app output attributable without
// changing the local runner's logging contract. Writes are serialized across
// apps and every new line starts with the manifest slug.
type prefixedWriter struct {
	mu        *sync.Mutex
	w         io.Writer
	prefix    string
	lineStart bool
}

// appEventWriter keeps multi-app NDJSON a valid event stream and adds the app
// identity every consumer needs to demultiplex concurrent fleet development.
type appEventWriter struct {
	mu  *sync.Mutex
	w   io.Writer
	app string
	buf bytes.Buffer
}

func newAppEventWriter(mu *sync.Mutex, w io.Writer, app string) *appEventWriter {
	return &appEventWriter{mu: mu, w: w, app: app}
}

func (w *appEventWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	_, _ = w.buf.Write(p)
	for {
		line, err := w.buf.ReadBytes('\n')
		if len(line) == 0 {
			return n, nil
		}
		if err != nil {
			// Put an incomplete record back for the next write.
			w.buf.Write(line)
			return n, nil
		}
		// RawMessage preserves every existing JSON value exactly—including int64
		// identifiers beyond JavaScript's safe-integer range—while adding only the
		// fleet identity needed to demultiplex the stream.
		var event map[string]json.RawMessage
		if json.Unmarshal(bytes.TrimSpace(line), &event) == nil {
			encodedApp, _ := json.Marshal(w.app)
			event["app"] = encodedApp
			if encodeErr := json.NewEncoder(w.w).Encode(event); encodeErr != nil {
				return 0, encodeErr
			}
			continue
		}
		if _, writeErr := w.w.Write(line); writeErr != nil {
			return 0, writeErr
		}
	}
}

func newPrefixedWriter(mu *sync.Mutex, w io.Writer, slug string) *prefixedWriter {
	return &prefixedWriter{mu: mu, w: w, prefix: "[" + slug + "] ", lineStart: true}
}

func (w *prefixedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written := 0
	for len(p) > 0 {
		if w.lineStart {
			if _, err := io.WriteString(w.w, w.prefix); err != nil {
				return written, err
			}
			w.lineStart = false
		}
		i := 0
		for i < len(p) && p[i] != '\n' {
			i++
		}
		if i < len(p) {
			i++
			w.lineStart = true
		}
		n, err := w.w.Write(p[:i])
		written += n
		if err != nil {
			return written, err
		}
		if n != i {
			return written, io.ErrShortWrite
		}
		p = p[i:]
	}
	return written, nil
}
