package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/process"
)

// defaultLogTail is the initial-burst line count when no ?tail= is given.
const defaultLogTail = 200

// maxLogTail caps user-requested ?tail= values to keep handler memory bounded.
// A LogReader.Tail call allocates a ring of up to N strings, so an unbounded
// query param would let an authenticated caller force the server to retain
// the entire (up to 5 MB rotated) log in memory per request.
const maxLogTail = 10000

// handleLogs returns log lines for the given app.
//
// The optional query params are:
//   - ?replica=N    (default 0) — which replica's log to read.
//   - ?tail=N       (default 200, 1..10000) — initial-burst line count.
//   - ?follow=BOOL  (default true) — when true emits SSE and follows new
//     output; when false returns a single plain-text response containing
//     the tailed lines and closes the connection. The plain-text shape is
//     the kubectl/docker `--no-follow` style, suitable for one-shot
//     scripted fetches without an SSE parser.
//
// Access is restricted to app managers (owners, admins, operators).
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}

	q := r.URL.Query()

	idx := 0
	if raw := q.Get("replica"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 255 {
			writeError(w, http.StatusBadRequest, "replica index out of range")
			return
		}
		idx = n
	}

	tail := defaultLogTail
	if raw := q.Get("tail"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 || n > maxLogTail {
			writeError(w, http.StatusBadRequest, "tail must be between 1 and "+strconv.Itoa(maxLogTail))
			return
		}
		tail = n
	}

	follow := true
	if raw := q.Get("follow"); raw != "" {
		switch raw {
		case "true", "1":
			follow = true
		case "false", "0":
			follow = false
		default:
			writeError(w, http.StatusBadRequest, "follow must be true or false")
			return
		}
	}

	if s.manager == nil {
		writeError(w, http.StatusNotFound, "no log available")
		return
	}
	lr, ok := s.manager.LogReader(slug, idx)
	if !ok {
		writeError(w, http.StatusNotFound, "no log available")
		return
	}

	if !follow {
		writeLogsPlain(w, lr, tail)
		return
	}
	streamLogReader(w, r, lr, tail, true)
}

// writeLogsPlain emits a one-shot, plain-text response: the last `tail` lines
// of the log, one per line, with a trailing newline. Suitable for scripted
// callers that pipe the output to tail/grep without parsing SSE frames.
func writeLogsPlain(w http.ResponseWriter, lr *process.LogReader, tail int) {
	lines, err := lr.Tail(tail)
	if err != nil {
		slog.Warn("logs tail", "err", err)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
}

// streamLogFile is a path-based wrapper used by per-run schedule log streaming.
func streamLogFile(w http.ResponseWriter, r *http.Request, path string, follow bool) {
	streamLogReader(w, r, process.NewLogReader(path), defaultLogTail, follow)
}

// writeLogFilePlain is a path-based wrapper for a one-shot plain-text dump of a
// log file, used by per-run schedule log fetches with follow=false.
func writeLogFilePlain(w http.ResponseWriter, path string, tail int) {
	writeLogsPlain(w, process.NewLogReader(path), tail)
}

// streamLogReader writes the SSE response: initial Tail(tail), then optionally
// Follow until the client disconnects, with periodic heartbeats.
// When follow is false, the tail is flushed and the connection is closed
// immediately — suitable for completed schedule runs whose log files are static.
func streamLogReader(w http.ResponseWriter, r *http.Request, lr *process.LogReader, tail int, follow bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Initial burst: last `tail` lines.
	lines, err := lr.Tail(tail)
	if err != nil {
		slog.Warn("logs tail", "err", err)
	}
	for _, line := range lines {
		fmt.Fprintf(w, "data: %s\n\n", line)
	}
	flusher.Flush()

	if !follow {
		return
	}

	// Follow new output until the client disconnects.
	ch := make(chan string, 64)
	go lr.Follow(r.Context(), ch)

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case line := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}
