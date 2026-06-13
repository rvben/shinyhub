package api

import (
	"net/http"
	"os/exec"
	"sync"
)

// serverInfoResponse is the JSON shape returned by GET /api/server-info.
type serverInfoResponse struct {
	// Version is the running shinyhub binary version. A fleet-aware CLI reads it
	// to distinguish a healthy shinyhub from a half-provisioned host (a front
	// proxy answering before the binary is up) and to enforce version
	// requirements before issuing a mutating call.
	Version      string             `json:"version"`
	Capabilities serverCapabilities `json:"capabilities"`
	// Runtimes reports which app runtimes the host can actually start, keyed by
	// language ("python", "r"). A developer (or the CLI) reads this to learn
	// that, e.g., an R deploy will fail because R is not installed - instead of
	// hitting an opaque deploy error.
	Runtimes map[string]bool `json:"runtimes"`
}

// serverCapabilities enumerates the optional protocol features this server
// supports. A fleet-aware CLI inspects these flags to decide whether it can
// rely on precondition headers and content-digest tracking, or must degrade
// gracefully against an older server.
type serverCapabilities struct {
	FleetPreconditions bool `json:"fleet_preconditions"`
	ContentDigest      bool `json:"content_digest"`
}

// handleServerInfo advertises server capability flags so a fleet-aware CLI
// can detect precondition + digest support and degrade gracefully against
// older servers. Unauthenticated and side-effect free.
func (s *Server) handleServerInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, serverInfoResponse{
		Version: s.version,
		Capabilities: serverCapabilities{
			FleetPreconditions: true,
			ContentDigest:      true,
		},
		Runtimes: detectRuntimes(),
	})
}

var (
	runtimesOnce  sync.Once
	runtimesCache map[string]bool
)

// detectRuntimes reports which app runtimes are available on the host PATH.
// Python apps run via uv (or python3) and R apps via Rscript, so each language
// is "available" when its launcher resolves. The result is cached after the
// first call: PATH does not change for the life of the process.
func detectRuntimes() map[string]bool {
	runtimesOnce.Do(func() {
		runtimesCache = map[string]bool{
			"python": onPath("uv") || onPath("python3"),
			"r":      onPath("Rscript"),
		}
	})
	return runtimesCache
}

func onPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// SetVersion records the binary version string advertised by GET
// /api/server-info. The parent binary calls this at startup so the server and
// the CLI subcommands report the same version.
func (s *Server) SetVersion(v string) { s.version = v }
