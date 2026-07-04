//go:build !linux

package process

import "errors"

// errReclaimUnsupported is returned on non-Linux hosts, where cgroup v2
// memory.reclaim does not exist. DockerRuntime.Suspend and NativeRuntime.Suspend
// treat it as "not freed" and the watcher falls back to Stop; NativeRuntime's
// ensureDelegatedBase failure disables native warm-wake entirely.
var errReclaimUnsupported = errors.New("cgroup memory.reclaim is only supported on linux")

func cgroupCurrentMemory(_ int) (uint64, error) { return 0, errReclaimUnsupported }

func reclaimPIDMemory(_ int, _ uint64) error { return errReclaimUnsupported }

// Native per-app cgroup helpers are linux-only; the stubs keep NativeRuntime
// (which is cross-platform) compiling on non-linux. ensureDelegatedBase failing
// here is exactly how native warm-wake stays off on non-linux hosts.
func ensureDelegatedBase() (string, bool, error) { return "", false, errReclaimUnsupported }

func setupAppCgroup(_, _ string, _ int) (string, error) { return "", errReclaimUnsupported }

func setCgroupMemoryMax(_ string, _ int) error { return errReclaimUnsupported }

func setCgroupCPUMax(_ string, _ int) error { return errReclaimUnsupported }

func setCgroupPidsMax(_ string, _ int) error { return errReclaimUnsupported }

func appCgroupCurrentMemory(_ string) (uint64, error) { return 0, errReclaimUnsupported }

func readAppCgroupOOMCount(_ string) uint64 { return 0 }

func killAppCgroupProcs(_ string) {}

func reclaimAppCgroup(_ string, _ uint64) error { return errReclaimUnsupported }

func teardownAppCgroup(_ string) error { return errReclaimUnsupported }
