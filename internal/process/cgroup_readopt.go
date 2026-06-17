package process

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// appCgroupName is the per-app cgroup directory name (relative to the delegated
// base) for a replica. It is the single source of truth for the layout, shared
// by Start (placeInAppCgroup) and re-adoption after a restart.
func appCgroupName(slug string, index int) string {
	return fmt.Sprintf("app-%s-%d", slug, index)
}

// appCgroupDir reconstructs the absolute per-app cgroup directory for a replica,
// matching the directory placeInAppCgroup creates at Start, so re-adoption after
// a restart finds the exact directory the prior process life created.
func appCgroupDir(base, slug string, index int) string {
	return filepath.Join(base, appCgroupName(slug, index))
}

// cgroupContainsPID reports whether pid is a member of the cgroup at dir by
// reading its cgroup.procs (one PID per line). A missing/unreadable file is an
// error so the caller treats the cgroup as gone and degrades to stop-hibernate
// rather than re-registering a stale directory. It is plain file I/O (no
// Linux-only syscall) so it builds and tests on every platform.
func cgroupContainsPID(dir string, pid int) (bool, error) {
	f, err := os.Open(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return false, fmt.Errorf("read cgroup.procs: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if n, perr := strconv.Atoi(sc.Text()); perr == nil && n == pid {
			return true, nil
		}
	}
	return false, sc.Err()
}
