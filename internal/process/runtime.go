package process

import (
	"context"
	"io"
	"syscall"
)

// Runtime abstracts how app processes are started and managed.
// NativeRuntime uses exec.Command; DockerRuntime uses the Docker Engine API.
type Runtime interface {
	// Start spawns a new process. logWriter receives combined stdout+stderr.
	Start(ctx context.Context, p StartParams, logWriter io.Writer) (RunHandle, error)
	// Signal sends sig to the process or container identified by handle.
	Signal(handle RunHandle, sig syscall.Signal) error
	// Wait blocks until the process or container identified by handle exits.
	Wait(ctx context.Context, handle RunHandle) error
	// Stats returns CPU usage (percent, 0–100+) and RSS bytes for the handle.
	Stats(ctx context.Context, handle RunHandle) (cpuPercent float64, rssBytes uint64, err error)
	// RunOnce spawns a short-lived process from the same bundle/runtime context
	// as Start, blocks until it exits or ctx is cancelled, and returns the
	// exit info. Implementations MUST signal SIGTERM on ctx cancel and
	// SIGKILL after a 10-second grace.
	RunOnce(ctx context.Context, p StartParams, logWriter io.Writer) (ExitInfo, error)
	// HostPreparesDeps reports whether bundle dependencies (uv sync,
	// renv::restore) should be installed on the host before Start. Native
	// runtimes use the host's PATH and need this; container runtimes prepare
	// deps inside the image/container, so callers must NOT touch the host.
	HostPreparesDeps() bool
	// AppBindHost reports the address an app process should bind its listening
	// socket to. Native and Docker host-network runtimes return "127.0.0.1" so
	// only the in-process proxy can reach the app. Docker bridge-network
	// runtimes return "0.0.0.0" so the published port mapping (which lives in
	// the container's separate network namespace) is reachable from the host.
	AppBindHost() string
}

// ExitInfo summarizes how a one-shot process ended.
type ExitInfo struct {
	Code     int  // exit code; -1 if Signaled
	Signaled bool // true if killed by signal (e.g. SIGKILL after timeout)
}

// SharedMount is a read-only mount of another app's data dir into the consumer.
type SharedMount struct {
	SourceSlug string // for path naming under data/shared/<source-slug>
	HostPath   string // absolute path on the host (the source app's app-data dir)
}

// RunHandle identifies a running app instance.
// Exactly one field is non-zero depending on the runtime in use.
type RunHandle struct {
	PID         int    // set by NativeRuntime
	ContainerID string // set by DockerRuntime
}

// ContainerInfo is a summary of a running container used during process recovery.
type ContainerInfo struct {
	ID     string
	Labels map[string]string
}
