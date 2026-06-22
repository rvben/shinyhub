//go:build linux

package process

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// cgroupV2Mount is the unified cgroup v2 mount point.
const cgroupV2Mount = "/sys/fs/cgroup"

// supervisorName is the leaf cgroup under the delegated base into which shinyhub
// moves its own process, so the base can delegate the memory controller to the
// per-app children (cgroup v2's no-internal-process rule).
const supervisorName = "_supervisor"

// cgroupCurrentMemory reads memory.current (bytes charged to the cgroup) for the
// cgroup that pid belongs to. Used to measure resident memory before/after a
// reclaim. It reads the cgroup file directly (not the docker stats API) so it is
// accurate while the container is paused.
func cgroupCurrentMemory(pid int) (uint64, error) {
	rel, err := cgroupV2RelPath(pid)
	if err != nil {
		return 0, err
	}
	b, err := os.ReadFile(filepath.Join(cgroupV2Mount, rel, "memory.current"))
	if err != nil {
		return 0, fmt.Errorf("read memory.current: %w", err)
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}

// reclaimPIDMemory asks the kernel to reclaim up to targetBytes from the cgroup
// that pid belongs to, by writing to that cgroup's memory.reclaim (cgroup v2,
// kernel >= 5.19). Anonymous pages move to swap; with no swap the kernel can
// only drop clean file pages, so little anon RAM is freed (the caller detects
// that via the RSS-drop threshold). Returns an error if cgroup v2 /
// memory.reclaim is unavailable or the write fails.
func reclaimPIDMemory(pid int, targetBytes uint64) error {
	rel, err := cgroupV2RelPath(pid)
	if err != nil {
		return err
	}
	return writeCgroupReclaim(filepath.Join(cgroupV2Mount, rel, "memory.reclaim"), targetBytes)
}

// writeCgroupReclaim asks the kernel to reclaim up to targetBytes from the cgroup
// whose memory.reclaim file is reclaimFile. Anonymous pages move to swap; with no
// swap the kernel can only drop clean file pages, so little anon RAM is freed (the
// caller detects that via the RSS-drop threshold).
//
// It writes via a raw blocking syscall on f.Fd() (which detaches the fd from the
// runtime netpoller). memory.reclaim returns EAGAIN when it reclaimed some but not
// the full requested amount - a regular *os.File write would park the goroutine on
// the poller waiting for the cgroup fd to become writable, which never happens. We
// treat EAGAIN as success: the kernel reclaimed what it could and the caller
// measures the actual drop against the threshold.
func writeCgroupReclaim(reclaimFile string, targetBytes uint64) error {
	f, err := os.OpenFile(reclaimFile, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", reclaimFile, err)
	}
	defer f.Close()
	data := []byte(strconv.FormatUint(targetBytes, 10))
	fd := int(f.Fd())
	for {
		// The returned byte count is kernel-specific for memory.reclaim and is
		// intentionally ignored. EAGAIN means the kernel reclaimed some but not
		// the full requested amount - acceptable, the caller measures the real
		// drop. Retry on EINTR.
		if _, werr := syscall.Write(fd, data); werr != nil {
			if werr == syscall.EINTR {
				continue
			}
			if werr != syscall.EAGAIN {
				return fmt.Errorf("write %s: %w", reclaimFile, werr)
			}
		}
		return nil
	}
}

// cgroupV2RelPath returns the cgroup-v2 path of pid relative to the v2 mount,
// parsed from /proc/<pid>/cgroup (the unified v2 line is "0::<path>").
func cgroupV2RelPath(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", fmt.Errorf("read /proc/%d/cgroup: %w", pid, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "0::") {
			return strings.TrimPrefix(line, "0::"), nil
		}
	}
	return "", fmt.Errorf("no cgroup v2 path for pid %d", pid)
}

// ensureDelegatedBase prepares shinyhub's own cgroup v2 subtree so per-app memory
// reclaim is possible, returning the absolute base directory under which per-app
// cgroups are created. This is the highest-care step: it moves shinyhub's own
// process between cgroups, so every step is verified and nothing is left partially
// applied.
//
// cgroup v2's "no internal process" rule means a cgroup that delegates a
// controller to its children may not itself hold processes. shinyhub's service
// cgroup (base) initially holds shinyhub, so we (1) require the memory controller
// to be delegated to base (systemd Delegate=memory), (2) move every process in
// base into base/_supervisor, then (3) enable +memory (and +cpu when available
// via Delegate=cpu) in base/cgroup.subtree_control so app children expose
// memory.current / memory.reclaim and can set memory.max / cpu.max. Idempotent:
// once memory is enabled (and cpu, when delegated) the subtree is prepared and
// base is returned immediately.
func ensureDelegatedBase() (string, error) {
	rel, err := cgroupV2RelPath(os.Getpid())
	if err != nil {
		return "", err
	}
	selfDir := filepath.Join(cgroupV2Mount, rel)
	// Resolve the base. On a first call shinyhub is still in its service cgroup,
	// so base == selfDir. On any later call shinyhub has already been moved into
	// base/_supervisor, so the real base is the parent - detect that (we only
	// ever create a child literally named "_supervisor") so re-invocation is
	// idempotent and never nests a second _supervisor under the first.
	base := selfDir
	if filepath.Base(selfDir) == supervisorName {
		base = filepath.Dir(selfDir)
	}

	subtreePath := filepath.Join(base, "cgroup.subtree_control")
	controllersPath := filepath.Join(base, "cgroup.controllers")
	memEnabled := cgroupHasField(subtreePath, "memory")
	// cpu is delegated best-effort: it lets per-app cgroups set cpu.max for native
	// CPU limits. It is absent without systemd Delegate=cpu, in which case CPU
	// limits degrade to no enforcement (memory limits + warm-wake still work).
	cpuAvail := cgroupHasField(controllersPath, "cpu")
	cpuEnabled := cgroupHasField(subtreePath, "cpu")
	// Already prepared: memory in subtree_control implies base holds no procs and
	// shinyhub already lives in _supervisor. Re-running is a no-op once memory is
	// enabled and cpu is enabled (or unavailable).
	if memEnabled && (cpuEnabled || !cpuAvail) {
		return base, nil
	}
	// The memory controller must be available to base. Without systemd
	// Delegate=memory it is absent, and warm-wake stays off (caller degrades).
	if !cgroupHasField(controllersPath, "memory") {
		return "", fmt.Errorf("cgroup %s: memory controller not delegated (need systemd Delegate=memory)", rel)
	}
	sup := filepath.Join(base, supervisorName)
	if err := os.Mkdir(sup, 0o755); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("mkdir %s: %w", sup, err)
	}
	// Empty base of processes (including shinyhub itself) before delegating
	// controllers; cgroup v2 rejects enabling a controller while procs remain.
	if err := drainCgroupProcs(base, sup); err != nil {
		return "", err
	}
	// Enable the controllers not yet delegated to children: memory (required) and
	// cpu (when available).
	var ctrls []string
	if !memEnabled {
		ctrls = append(ctrls, "+memory")
	}
	if cpuAvail && !cpuEnabled {
		ctrls = append(ctrls, "+cpu")
	}
	if len(ctrls) > 0 {
		if err := writeCgroupFile(subtreePath, strings.Join(ctrls, " ")); err != nil {
			return "", fmt.Errorf("enable %s on %s: %w", strings.Join(ctrls, " "), base, err)
		}
	}
	return base, nil
}

// setupAppCgroup creates base/<name> and moves pid into it, returning the app
// cgroup's absolute directory. The caller tears it down on stop/exit.
func setupAppCgroup(base, name string, pid int) (string, error) {
	dir := filepath.Join(base, name)
	if err := os.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if err := writeCgroupProc(dir, pid); err != nil {
		_ = os.Remove(dir)
		return "", fmt.Errorf("place pid %d in %s: %w", pid, dir, err)
	}
	return dir, nil
}

// setCgroupMemoryMax writes a memory.max limit (memMB mebibytes, or "max" when
// memMB <= 0) to an app cgroup directory created by setupAppCgroup. The memory
// controller must be delegated to the base (it is, when the base came up), so a
// write failure is a real error worth surfacing rather than silently dropping
// the limit.
func setCgroupMemoryMax(dir string, memMB int) error {
	return writeCgroupFile(filepath.Join(dir, "memory.max"), cgroupMemoryMaxValue(memMB))
}

// setCgroupCPUMax writes a cpu.max limit (cpuPct percent of one core, or no
// limit when cpuPct <= 0) to an app cgroup directory. The cpu controller must be
// delegated to the base (systemd Delegate=cpu) for the cpu.max file to exist, so
// a write failure is surfaced for the caller to warn on rather than silently
// dropping the limit.
func setCgroupCPUMax(dir string, cpuPct int) error {
	return writeCgroupFile(filepath.Join(dir, "cpu.max"), cgroupCPUMaxValue(cpuPct))
}

// appCgroupCurrentMemory reads memory.current (bytes charged) for an app cgroup
// directory created by setupAppCgroup.
func appCgroupCurrentMemory(dir string) (uint64, error) {
	b, err := os.ReadFile(filepath.Join(dir, "memory.current"))
	if err != nil {
		return 0, fmt.Errorf("read memory.current: %w", err)
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}

// reclaimAppCgroup reclaims up to targetBytes from an app cgroup directory.
func reclaimAppCgroup(dir string, targetBytes uint64) error {
	return writeCgroupReclaim(filepath.Join(dir, "memory.reclaim"), targetBytes)
}

// teardownAppCgroup rmdir's an app cgroup once its process has exited. A cgroup
// can only be removed when it holds no processes; the kernel removes the exited
// pid asynchronously, so a brief EBUSY is retried. A missing dir is success.
func teardownAppCgroup(dir string) error {
	var err error
	for i := 0; i < 20; i++ {
		err = os.Remove(dir)
		if err == nil || os.IsNotExist(err) {
			return nil
		}
		if !errors.Is(err, syscall.EBUSY) {
			return fmt.Errorf("rmdir %s: %w", dir, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("rmdir %s: still busy after retries: %w", dir, err)
}

// cgroupHasField reports whether the space-separated list in path (e.g.
// cgroup.controllers or cgroup.subtree_control) contains field.
func cgroupHasField(path, field string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, f := range strings.Fields(string(b)) {
		if f == field {
			return true
		}
	}
	return false
}

// writeCgroupFile writes s to a cgroup control file (no trailing newline needed;
// the kernel parses the token directly).
func writeCgroupFile(path, s string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(s)
	return err
}

// writeCgroupProc moves pid into the cgroup directory dir by writing it to
// dir/cgroup.procs (which moves the whole process and all its threads).
func writeCgroupProc(dir string, pid int) error {
	return writeCgroupFile(filepath.Join(dir, "cgroup.procs"), strconv.Itoa(pid))
}

// drainCgroupProcs moves every process in from into to, then verifies from is
// empty. A process that exits between the read and the move (ESRCH) is skipped.
// It retries a few times to absorb a process appearing mid-drain, and fails if
// from still holds processes afterward.
func drainCgroupProcs(from, to string) error {
	for attempt := 0; attempt < 5; attempt++ {
		pids, err := readCgroupProcs(filepath.Join(from, "cgroup.procs"))
		if err != nil {
			return err
		}
		if len(pids) == 0 {
			return nil
		}
		for _, pid := range pids {
			if err := writeCgroupProc(to, pid); err != nil {
				if errors.Is(err, syscall.ESRCH) {
					continue
				}
				return fmt.Errorf("move pid %d into %s: %w", pid, to, err)
			}
		}
	}
	return fmt.Errorf("cgroup %s still has processes after drain", from)
}

// readCgroupProcs parses the PIDs listed in a cgroup.procs file.
func readCgroupProcs(path string) ([]int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var pids []int
	for _, field := range strings.Fields(string(b)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			return nil, fmt.Errorf("parse pid %q in %s: %w", field, path, err)
		}
		pids = append(pids, pid)
	}
	return pids, nil
}
