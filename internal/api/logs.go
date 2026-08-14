package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

// defaultLogTail is the initial-burst line count when no ?tail= is given.
const defaultLogTail = 200

// maxLogTail caps user-requested ?tail= values to keep handler memory bounded.
// A LogReader.Tail call allocates a ring of up to N strings, so an unbounded
// query param would let an authenticated caller force the server to retain
// the entire (up to 5 MB rotated) log in memory per request.
const maxLogTail = 10000

type appLogReader interface {
	Tail(int) ([]string, error)
	Follow(context.Context, chan<- string)
}

type logSourceResponse struct {
	SourceID     string     `json:"source_id"`
	RunID        string     `json:"run_id,omitempty"`
	Replica      int        `json:"replica"`
	Current      bool       `json:"current"`
	Legacy       bool       `json:"legacy,omitempty"`
	Status       string     `json:"status"`
	Provider     string     `json:"provider,omitempty"`
	Tier         string     `json:"tier,omitempty"`
	AppVersion   string     `json:"app_version,omitempty"`
	DeploymentID *int64     `json:"deployment_id,omitempty"`
	UpdatedAt    time.Time  `json:"updated_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	OOMKilled    bool       `json:"oom_killed,omitempty"`
	LogUpdatedAt *time.Time `json:"log_updated_at,omitempty"`
	SizeBytes    int64      `json:"size_bytes"`
	HasLog       bool       `json:"has_log"`
	// StreamAvailable is false for runtimes whose application output is retained
	// by an external provider rather than copied into ShinyHub's local log file.
	StreamAvailable bool `json:"stream_available"`
}

// handleLogSources lists both live replica rows and retained on-disk logs. A
// scaled-down replica no longer has a DB row, but its log remains useful and is
// surfaced as stopped. Conversely a just-starting replica can have metadata
// before it emits its first byte, so it is returned with has_log=false.
func (s *Server) handleLogSources(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
		return
	}
	if s.manager == nil {
		writeError(w, http.StatusNotFound, "no log sources available")
		return
	}

	replicas, err := s.store.ListReplicas(app.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	replicaByIndex := make(map[int]*db.Replica, len(replicas))
	for _, rep := range replicas {
		replicaByIndex[rep.Index] = rep
	}
	liveByIndex := make(map[int]*process.ProcessInfo)
	for _, live := range s.manager.AllForSlug(slug) {
		if live == nil {
			continue
		}
		liveByIndex[live.Index] = live
	}

	files, err := s.manager.LogRuns(slug)
	if err != nil {
		slog.Warn("list log runs", "slug", slug, "err", err)
		writeError(w, http.StatusInternalServerError, "could not list log sources")
		return
	}
	fileByRun := make(map[string]process.LogSource, len(files))
	for _, file := range files {
		fileByRun[file.RunID] = file
	}

	runs, err := s.store.ListAppLogRuns(app.ID, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	runIDs := make([]string, len(runs))
	for i, run := range runs {
		runIDs[i] = run.RunID
	}
	sharedStats, err := s.store.AppLogStatsForRuns(runIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	out := make([]*logSourceResponse, 0, len(runs)+len(replicas))
	seenCurrent := make(map[int]bool)
	for _, run := range runs {
		current := !seenCurrent[run.ReplicaIndex]
		seenCurrent[run.ReplicaIndex] = true
		started := run.StartedAt
		src := &logSourceResponse{
			SourceID: run.RunID, RunID: run.RunID, Replica: run.ReplicaIndex,
			Current: current, Status: run.Status, Provider: run.Provider,
			Tier: run.Tier, AppVersion: run.AppVersion,
			DeploymentID: run.DeploymentID, UpdatedAt: run.StartedAt,
			StartedAt: &started, FinishedAt: run.FinishedAt, OOMKilled: run.OOMKilled,
		}
		if file, ok := fileByRun[run.RunID]; ok {
			modified := file.ModifiedAt
			src.HasLog = true
			src.SizeBytes = file.SizeBytes
			src.LogUpdatedAt = &modified
		}
		if stats, ok := sharedStats[run.RunID]; ok {
			updated := stats.UpdatedAt
			src.HasLog = stats.SizeBytes > 0
			src.SizeBytes = stats.SizeBytes
			src.LogUpdatedAt = &updated
		}
		if current {
			applyCurrentLogSourceState(src, replicaByIndex[run.ReplicaIndex], liveByIndex[run.ReplicaIndex])
		}
		src.StreamAvailable = src.Provider != "fargate"
		out = append(out, src)
	}

	legacy, err := s.manager.LogSources(slug)
	if err != nil {
		slog.Warn("list legacy log sources", "slug", slug, "err", err)
		writeError(w, http.StatusInternalServerError, "could not list log sources")
		return
	}
	for _, log := range legacy {
		modified := log.ModifiedAt
		current := !seenCurrent[log.Index]
		src := &logSourceResponse{
			SourceID: fmt.Sprintf("legacy-%d", log.Index), Replica: log.Index,
			Current: current, Legacy: true, Status: "stopped", UpdatedAt: modified,
			LogUpdatedAt: &modified, SizeBytes: log.SizeBytes, HasLog: true,
			StreamAvailable: true,
		}
		if current {
			applyCurrentLogSourceState(src, replicaByIndex[log.Index], liveByIndex[log.Index])
		}
		out = append(out, src)
	}

	// A provider may expose a live replica before its first local byte or run
	// record (notably externally-retained Fargate output). Keep it visible.
	for index, rep := range replicaByIndex {
		if seenCurrent[index] {
			continue
		}
		foundLegacy := false
		for _, src := range out {
			if src.Replica == index && src.Current {
				foundLegacy = true
				break
			}
		}
		if foundLegacy {
			continue
		}
		src := &logSourceResponse{SourceID: fmt.Sprintf("replica-%d", index), Replica: index, Current: true}
		applyCurrentLogSourceState(src, rep, liveByIndex[index])
		src.StreamAvailable = src.Provider != "fargate"
		out = append(out, src)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Current != out[j].Current {
			return out[i].Current
		}
		if out[i].Current && out[i].Replica != out[j].Replica {
			return out[i].Replica < out[j].Replica
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	writeJSON(w, http.StatusOK, map[string]any{"sources": out})
}

func applyCurrentLogSourceState(src *logSourceResponse, rep *db.Replica, live *process.ProcessInfo) {
	if rep != nil {
		src.Status, src.Provider, src.Tier = rep.Status, rep.Provider, rep.Tier
		src.AppVersion, src.DeploymentID, src.UpdatedAt = rep.AppVersion, rep.DeploymentID, rep.UpdatedAt
	}
	if live != nil {
		src.Status = string(live.Status)
		if live.Provider != "" {
			src.Provider = live.Provider
		}
		if live.Tier != "" {
			src.Tier = live.Tier
		}
		if live.AppVersion != "" {
			src.AppVersion = live.AppVersion
		}
		if live.DeploymentID != 0 {
			id := live.DeploymentID
			src.DeploymentID = &id
		}
	}
	if src.Status == "" {
		src.Status = "unknown"
	}
}

// handleLogs returns log lines for the given app.
//
// The optional query params are:
//   - ?replica=N    (default 0) — which replica's latest log to read.
//   - ?run=UUID     — an immutable execution, or "legacy" for pre-upgrade logs.
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
	app, ok := s.requireManageApp(w, r, slug)
	if !ok {
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
	var lr appLogReader
	var hasReader bool
	if runID := q.Get("run"); runID != "" {
		if runID == "legacy" {
			lr, hasReader = s.manager.LegacyLogReader(slug, idx)
		} else {
			run, err := s.store.GetAppLogRun(app.ID, runID)
			if err != nil || run.ReplicaIndex != idx {
				writeError(w, http.StatusNotFound, "no log available")
				return
			}
			_, hasSharedLog, statsErr := s.store.AppLogEndOffset(runID)
			if statsErr != nil {
				writeError(w, http.StatusInternalServerError, "internal server error")
				return
			}
			if hasSharedLog || (s.store.IsPostgres() && run.Provider != "fargate") {
				lr, hasReader = s.store.NewAppLogReader(runID), true
			} else {
				lr, hasReader = s.manager.LogRunReader(slug, idx, runID)
			}
		}
	} else if s.store.IsPostgres() {
		runs, err := s.store.ListAppLogRuns(app.ID, 100)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		for _, run := range runs {
			if run.ReplicaIndex == idx && run.Provider != "fargate" {
				lr, hasReader = s.store.NewAppLogReader(run.RunID), true
				break
			}
		}
	} else {
		lr, hasReader = s.manager.LogReader(slug, idx)
	}
	if !hasReader {
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
func writeLogsPlain(w http.ResponseWriter, lr appLogReader, tail int) {
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
func streamLogReader(w http.ResponseWriter, r *http.Request, lr appLogReader, tail int, follow bool) {
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
