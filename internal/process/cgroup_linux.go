//go:build linux

package process

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// cgroupV2Mount is the unified cgroup v2 mount point.
const cgroupV2Mount = "/sys/fs/cgroup"

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
	reclaimFile := filepath.Join(cgroupV2Mount, rel, "memory.reclaim")
	f, err := os.OpenFile(reclaimFile, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", reclaimFile, err)
	}
	defer f.Close()
	// Write via a raw blocking syscall on f.Fd() (which detaches the fd from the
	// runtime netpoller). memory.reclaim returns EAGAIN when it reclaimed some but
	// not the full requested amount - a regular *os.File write would park the
	// goroutine on the poller waiting for the cgroup fd to become writable, which
	// never happens. We treat EAGAIN as success: the kernel reclaimed what it
	// could and the caller measures the actual RSS drop against the threshold.
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
