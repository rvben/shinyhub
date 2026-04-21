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

// handleLogs streams log lines for the given app as Server-Sent Events.
// It sends the last 200 lines as an initial burst, then follows new output.
// Access is restricted to app managers (owners, admins, operators).
// The optional query param ?replica=N (default 0) selects which replica's log to stream.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}

	idx := 0
	if raw := r.URL.Query().Get("replica"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 255 {
			writeError(w, http.StatusBadRequest, "replica index out of range")
			return
		}
		idx = n
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

	streamLogReader(w, r, lr, true)
}

// streamLogFile is a path-based wrapper used by per-run schedule log streaming.
func streamLogFile(w http.ResponseWriter, r *http.Request, path string, follow bool) {
	streamLogReader(w, r, process.NewLogReader(path), follow)
}

// streamLogReader writes the SSE response: initial Tail(200), then optionally
// Follow until the client disconnects, with periodic heartbeats.
// When follow is false, the tail is flushed and the connection is closed
// immediately — suitable for completed schedule runs whose log files are static.
func streamLogReader(w http.ResponseWriter, r *http.Request, lr *process.LogReader, follow bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Initial burst: last 200 lines.
	lines, err := lr.Tail(200)
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
