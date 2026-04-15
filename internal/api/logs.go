package api

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// handleLogs streams log lines for the given app as Server-Sent Events.
// It sends the last 200 lines as an initial burst, then follows new output.
// Access is restricted to app managers (owners, admins, operators).
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if _, ok := s.requireManageApp(w, r, slug); !ok {
		return
	}

	if s.manager == nil {
		writeError(w, http.StatusNotFound, "no log available")
		return
	}
	lr, ok := s.manager.LogReader(slug)
	if !ok {
		writeError(w, http.StatusNotFound, "no log available")
		return
	}

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
		log.Printf("logs: tail %s: %v", slug, err)
	}
	for _, line := range lines {
		fmt.Fprintf(w, "data: %s\n\n", line)
	}
	flusher.Flush()

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
