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
