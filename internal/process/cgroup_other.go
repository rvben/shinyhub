//go:build !linux

package process

import "errors"

// errReclaimUnsupported is returned on non-Linux hosts, where cgroup v2
// memory.reclaim does not exist. DockerRuntime.Suspend treats it as "not freed"
// and the watcher falls back to Stop.
var errReclaimUnsupported = errors.New("cgroup memory.reclaim is only supported on linux")

func cgroupCurrentMemory(_ int) (uint64, error) { return 0, errReclaimUnsupported }

func reclaimPIDMemory(_ int, _ uint64) error { return errReclaimUnsupported }
