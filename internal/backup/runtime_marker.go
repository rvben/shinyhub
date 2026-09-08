package backup

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

// runtimeMarkerSuffix names the liveness marker relative to the database file.
const runtimeMarkerSuffix = "-running.json"

// RuntimeMarker is what a running server publishes about itself.
type RuntimeMarker struct {
	PID       int       `json:"pid"`
	Addr      string    `json:"addr"`
	StartedAt time.Time `json:"started_at"`
}

// RuntimeMarkerPath returns where a running server publishes its liveness
// marker for cfg, or ok=false when cfg's database is not a local file.
//
// The marker sits beside the database file on purpose. The other two signals
// are both reachable only through configuration a maintenance command can get
// wrong without noticing: server.pid_file is opt-in and usually unset, and the
// listener probe is built from server.host/server.port, which fall back to
// 0.0.0.0:8080 and so probe the wrong address whenever restore is invoked with
// only the storage settings. The database path is the one setting restore
// cannot get wrong, because getting it wrong means restoring a different
// instance.
//
// Postgres has no such path, so no marker is published there; that deployment
// keeps the pid-file and listener signals, and pg_restore fails loudly on an
// in-use database rather than corrupting it silently.
func RuntimeMarkerPath(cfg *config.Config) (string, bool) {
	p, ok := dbFilePath(cfg.Database.DSN)
	if !ok {
		return "", false
	}
	return p + runtimeMarkerSuffix, true
}

// PublishRuntimeMarker records this process as the live server for cfg and
// returns a function that withdraws the record. Publishing is best effort in
// one direction only: a marker that cannot be written is reported as an error
// so a deployment does not quietly lose the guard, but the returned clear
// function never fails, since a shutdown must not be blocked by it.
//
// clear removes the marker only while it still names this process. A
// zero-downtime upgrade overlaps two processes: the successor republishes
// under its own PID, and the predecessor's clear then leaves it alone rather
// than blanking the live server's marker on its way out.
func PublishRuntimeMarker(cfg *config.Config) (clear func(), err error) {
	path, ok := RuntimeMarkerPath(cfg)
	if !ok {
		return func() {}, nil
	}
	me := os.Getpid()
	body, err := json.Marshal(RuntimeMarker{
		PID:       me,
		Addr:      net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port)),
		StartedAt: time.Now().UTC(),
	})
	if err != nil {
		return nil, fmt.Errorf("encode runtime marker: %w", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return nil, fmt.Errorf("write runtime marker %s: %w", path, err)
	}
	return func() {
		if m, ok := readRuntimeMarker(path); ok && m.PID == me {
			_ = os.Remove(path)
		}
	}, nil
}

// readRuntimeMarker reads the marker at path. A missing, unreadable, malformed
// or nonsensical marker reports ok=false: an unreadable marker must not be
// mistaken for a running server, since that would block every restore forever
// with no way to tell why.
func readRuntimeMarker(path string) (RuntimeMarker, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RuntimeMarker{}, false
	}
	var m RuntimeMarker
	if err := json.Unmarshal(data, &m); err != nil || m.PID <= 0 {
		return RuntimeMarker{}, false
	}
	return m, true
}

// liveRuntimeMarker reports the marker for cfg only when the process it names
// is still alive. A marker left by a crashed server names a dead PID and is
// ignored, matching how a stale pid_file is treated.
func liveRuntimeMarker(cfg *config.Config) (path string, m RuntimeMarker, ok bool) {
	path, ok = RuntimeMarkerPath(cfg)
	if !ok {
		return "", RuntimeMarker{}, false
	}
	m, ok = readRuntimeMarker(path)
	if !ok || !pidAlive(m.PID) {
		return "", RuntimeMarker{}, false
	}
	return path, m, true
}
